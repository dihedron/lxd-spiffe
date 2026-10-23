package lxdinstance

import (
	"context"
	"encoding/hex"
	"maps"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/canonical/lxd/shared/api"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	"google.golang.org/protobuf/proto"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/policy"
	proofpkg "github.com/dihedron/lxd-spiffe/internal/proof"
	lxdinstancev1 "github.com/dihedron/lxd-spiffe/internal/proto"
)

const (
	reasonPreconditionFailed = "precondition_failed"
	// maxRetries, retryBase and retryCap bound the 412 retries (FR-S14).
	maxRetries = 10
	retryBase  = 50 * time.Millisecond
	retryCap   = time.Second
)

// challenges are the config_push keys of the attestations running in this
// plugin process, by instance: a challenge write keeps them while it
// removes the instance's stale keys (Mode config_push, step 1). They
// outlive Configure, like the attestations.
type challenges struct {
	mu   sync.Mutex
	keys map[string]map[string]bool // instance: server/project/name
}

func newChallenges() *challenges {
	return &challenges{keys: map[string]map[string]bool{}}
}

// add records key for instance and returns its removal.
func (c *challenges) add(instance, key string) (remove func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys[instance] == nil {
		c.keys[instance] = map[string]bool{}
	}
	c.keys[instance][key] = true
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.keys[instance], key)
		if len(c.keys[instance]) == 0 {
			delete(c.keys, instance)
		}
	}
}

func (c *challenges) running(instance string) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.keys[instance])
}

// configPush runs the config_push challenge (Mode config_push): the key
// derived from the agent nonce (SEC-04) is written with hex(server nonce)
// under If-Match, stale challenge keys removed by the same write; the
// agent's echo must equal the value; the key is removed afterwards
// whatever the outcome, once the write succeeded.
func (a *attestation) configPush(ctx context.Context, m *match, r *lxdinstancev1.AttestRequest) error {
	serverNonce, err := proofpkg.NewNonce()
	if err != nil {
		return fail(reasonPolicyDenied, "%v", err)
	}
	key, err := proofpkg.Key(r.GetAgentNonce())
	if err != nil {
		return fail(reasonPayloadInvalid, "%v", err)
	}
	value := hex.EncodeToString(serverNonce)
	instance := m.srv.id + "/" + m.inst.Project + "/" + m.instance
	defer a.inflight.add(instance, key)()
	start := time.Now()
	defer func() { a.proof = time.Since(start) }()
	deadline := start.Add(a.s.proofTimeout)
	wctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	stale := 0
	write := func(inst *api.Instance) map[string]*string {
		config := map[string]*string{key: &value}
		running := a.inflight.running(instance)
		for k := range inst.Config {
			if strings.HasPrefix(k, proofpkg.KeyPrefix) && !running[k] {
				config[k] = nil
			}
		}
		stale = len(config) - 1
		return config
	}
	if err := a.patch(wctx, m, write); err != nil {
		return err // a failed write leaves nothing to clean up
	}
	a.log = a.log.With("config_push_stale_keys_removed_total", stale)
	defer a.cleanup(ctx, m, key)
	challenge := &lxdinstancev1.Challenge{
		Version:   lxdinstancev1.Version,
		Mode:      lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH,
		ProofKey:  key,
		TimeoutMs: uint32(time.Until(deadline).Milliseconds()), //nolint:gosec // proof_timeout is at most 2 minutes
	}
	data, err := proto.Marshal(challenge)
	if err != nil {
		return fail(reasonPolicyDenied, "%v", err)
	}
	if err := a.stream.Send(&nodeattestorv1.AttestResponse{Response: &nodeattestorv1.AttestResponse_Challenge{Challenge: data}}); err != nil {
		return fail(reasonProofTimeout, "sending the challenge: %v", err)
	}
	resp, err := a.receive(deadline)
	if err != nil {
		return err
	}
	if resp.GetMode() != challenge.GetMode() || !resp.GetDone() || !proofpkg.Equal(resp.GetEcho(), []byte(value)) {
		return fail(reasonProofMismatch, "the agent's echo is not the challenge value")
	}
	return nil
}

// cleanup removes the challenge key (Mode config_push, step 3), under the
// same 412 rules, within lxdSlack even when the attestation's context has
// ended; a failure is logged and counted, not returned (FR-S13).
func (a *attestation) cleanup(ctx context.Context, m *match, key string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lxdSlack)
	defer cancel()
	inst, etag, err := m.client.Instance(cctx, m.inst.Project, m.instance)
	m.srv.observe(cctx, err)
	if err == nil {
		err = a.patch(cctx, &match{srv: m.srv, client: m.client, inst: inst, etag: etag, instance: m.instance}, func(*api.Instance) map[string]*string {
			return map[string]*string{key: nil}
		})
	}
	if err != nil {
		a.log.Warn("config_push cleanup failed", "config_push_cleanup_failures_total", 1, "error", err.Error())
	}
}

// patch writes config(instance) under If-Match (FR-S14). On 412 it reads
// the instance again, applies every check of the decision to it (FR-S5),
// compares it with the first read, and retries with backoff and jitter, at
// most maxRetries times within ctx. m.inst and m.etag are those of the
// first write.
func (a *attestation) patch(ctx context.Context, m *match, config func(*api.Instance) map[string]*string) error {
	inst, etag := m.inst, m.etag
	wait := retryBase
	for retry := 0; ; retry++ {
		err := m.client.PatchConfig(ctx, m.inst.Project, m.instance, etag, config(inst))
		m.srv.observe(ctx, err)
		switch {
		case err == nil:
			return nil
		case retry > 0 && ctx.Err() != nil:
			return fail(reasonPreconditionFailed, "retries cut by proof_timeout: %v", err)
		case lxd.KindOf(err) != lxd.KindPrecondition:
			return fail(reasonLXDUnavailable, "writing the challenge key: %v", err)
		case retry == maxRetries:
			return fail(reasonPreconditionFailed, "the instance changed during %d retries", maxRetries)
		}
		jittered := wait/2 + rand.N(wait/2+1) //nolint:gosec // G404: jitter, not a secret
		select {
		case <-ctx.Done():
			return fail(reasonPreconditionFailed, "retries cut by proof_timeout: %v", ctx.Err())
		case <-time.After(jittered):
		}
		wait = min(2*wait, retryCap)
		if inst, etag, err = m.client.Instance(ctx, m.inst.Project, m.instance); err != nil {
			m.srv.observe(ctx, err)
			return fail(reasonLXDUnavailable, "reading the instance after a 412: %v", err)
		}
		if err := a.same(m, inst); err != nil {
			return err
		}
	}
}

// same applies the decision's checks to a re-read instance and requires
// the identity, project, name and location of the first read (FR-S14 (2)).
func (a *attestation) same(m *match, again *api.Instance) error {
	if _, err := policy.Check(again, a.policy, policy.ConfigPush, a.hint); err != nil {
		return denied(err, "after a 412")
	}
	return unchanged(m.inst, again, "after a 412")
}
