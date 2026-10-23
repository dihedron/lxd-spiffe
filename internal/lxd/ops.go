package lxd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"uuid"

	lxdclient "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/limits"
)

// The attestor's operations (FR-S17, minimal API contract): typed SDK calls
// bound to the caller's context, and raw requests where the SDK has no
// method or its method ignores the context (the files API).

// RequiredExtensions are the API extensions the attestor uses (FR-S12).
var RequiredExtensions = []string{"projects", "instance_generation_id", "api_filtering"}

// ErrMissingExtensions wraps CheckServer's error for a server that lacks a
// required API extension (reason extension_missing).
var ErrMissingExtensions = errors.New("lxd: the server lacks API extensions")

// agentNotRunning is LXD's error while a VM's lxd-agent is not up (L7).
const agentNotRunning = "LXD VM agent is not currently running"

// Server reads GET /1.0.
func (c *Client) Server(ctx context.Context) (*api.Server, error) {
	sdk, err := c.bound(ctx, "")
	if err != nil {
		return nil, err
	}
	srv, _, err := sdk.GetServer()
	return srv, mapError(ctx, err)
}

// CheckServer refuses a server that does not trust the client or lacks an
// extension (FR-S12); the error names the missing extensions.
func CheckServer(srv *api.Server, extensions ...string) error {
	if srv.Auth != "trusted" {
		return errors.New("lxd: the server does not trust this client")
	}
	var missing []string
	for _, ext := range extensions {
		if !slices.Contains(srv.APIExtensions, ext) {
			missing = append(missing, ext)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w %s", ErrMissingExtensions, strings.Join(missing, ", "))
	}
	return nil
}

// Instance reads an instance and its ETag, the one read all decisions come
// from (FR-S4).
func (c *Client) Instance(ctx context.Context, project, name string) (*api.Instance, string, error) {
	sdk, err := c.bound(ctx, project)
	if err != nil {
		return nil, "", err
	}
	inst, etag, err := sdk.GetInstance(name)
	if err != nil {
		return nil, "", mapError(ctx, err)
	}
	if inst.Name != name || (inst.Project != "" && inst.Project != project) ||
		(inst.Type != string(api.InstanceTypeContainer) && inst.Type != string(api.InstanceTypeVM)) {
		return nil, "", &Error{Kind: KindProtocol, Err: errors.New("instance read: name, project or type not as requested")}
	}
	return inst, etag, nil
}

// File is a file read through the files API: what its headers say and its
// content.
type File struct {
	UID, GID int64
	// Mode holds every bit X-LXD-mode reports, not only the permissions.
	Mode    int
	Type    string // file, directory or symlink
	Content []byte
}

// File reads a file of an instance. It does not use the SDK's
// GetInstanceFile, whose request carries neither the context nor its body
// limit; the body is limited to the files API's 4 KiB (FR-S17 (6)).
func (c *Client) File(ctx context.Context, project, name, path string) (*File, error) {
	resp, err := c.Raw(ctx, RawRequest{
		Method:   http.MethodGet,
		Path:     "/1.0/instances/" + url.PathEscape(name) + "/files",
		Project:  project,
		Query:    url.Values{"path": {path}},
		MaxBytes: limits.MaxFilesResponse,
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, responseError(resp)
	}
	f, err := parseFileHeaders(resp.Header)
	if err != nil {
		return nil, &Error{Kind: KindProtocol, Status: resp.Status, Err: err}
	}
	f.Content = resp.Body
	return f, nil
}

// InstancesByUUID lists the names of the project's instances with this
// volatile.uuid (FR-S18). The UUID goes into a filter expression, so only a
// canonical UUID is accepted.
func (c *Client) InstancesByUUID(ctx context.Context, project, id string) ([]string, error) {
	if u, err := uuid.Parse(id); err != nil || u.String() != id {
		return nil, errors.New("lxd: not a canonical UUID")
	}
	resp, err := c.Raw(ctx, RawRequest{
		Method:  http.MethodGet,
		Path:    "/1.0/instances",
		Project: project,
		Query:   url.Values{"filter": {"config.volatile.uuid eq " + id}, "recursion": {"1"}},
	})
	if err != nil {
		return nil, err
	}
	var list []api.Instance
	if err := Decode(resp, &list); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list))
	for _, inst := range list {
		if inst.Name == "" {
			return nil, &Error{Kind: KindProtocol, Err: errors.New("instance list: an instance without a name")}
		}
		names = append(names, inst.Name)
	}
	return names, nil
}

// PatchConfig changes config keys of an instance with PATCH under
// If-Match etag (FR-S13, FR-S14); a nil value removes the key. LXD answers
// a PATCH synchronously (gate L14); should it answer with an operation, the
// operation is waited for within ctx. A stale ETag is KindPrecondition.
func (c *Client) PatchConfig(ctx context.Context, project, name, etag string, config map[string]*string) error {
	body, err := json.Marshal(map[string]any{"config": config})
	if err != nil {
		return fmt.Errorf("lxd: encoding the config: %w", err)
	}
	resp, err := c.Raw(ctx, RawRequest{Method: http.MethodPatch, Path: "/1.0/instances/" + url.PathEscape(name), Project: project, Body: body, ETag: etag})
	if err != nil {
		return err
	}
	switch resp.Status {
	case http.StatusOK:
		return nil
	case http.StatusAccepted:
		return c.wait(ctx, project, resp)
	}
	return responseError(resp)
}

// wait waits for the operation of an async response to succeed.
func (c *Client) wait(ctx context.Context, project string, resp *RawResponse) error {
	env, err := resp.Envelope()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(env.Operation, "/1.0/operations/") {
		return &Error{Kind: KindProtocol, Status: resp.Status, Err: fmt.Errorf("operation %q", env.Operation)}
	}
	done, err := c.Raw(ctx, RawRequest{Method: http.MethodGet, Path: env.Operation + "/wait", Project: project})
	if err != nil {
		return err
	}
	var op api.Operation
	if err := Decode(done, &op); err != nil {
		return err
	}
	if op.Status != api.Success.String() {
		return &Error{Kind: KindServer, Status: done.Status, Err: fmt.Errorf("operation %s: %s: %s", op.ID, op.Status, op.Err)}
	}
	return nil
}

// Project reads a project and its ETag (gate L16: features.networks).
func (c *Client) Project(ctx context.Context, name string) (*api.Project, string, error) {
	sdk, err := c.bound(ctx, "")
	if err != nil {
		return nil, "", err
	}
	p, etag, err := sdk.GetProject(name)
	if err != nil {
		return nil, "", mapError(ctx, err)
	}
	if p.Name != name {
		return nil, "", &Error{Kind: KindProtocol, Err: errors.New("project read: name not as requested")}
	}
	return p, etag, nil
}

// CurrentIdentity reads the client's own identity (gate L9); an error of
// kind not-found where the server has no such endpoint.
func (c *Client) CurrentIdentity(ctx context.Context) (*api.IdentityInfo, error) {
	sdk, err := c.bound(ctx, "")
	if err != nil {
		return nil, err
	}
	info, _, err := sdk.GetCurrentIdentityInfo()
	return info, mapError(ctx, err)
}

// bound returns the SDK connection, in project when one is given, with its
// requests bound to ctx (FR-S17 (2)). WithContext is a method of the SDK's
// connection type, not of its InstanceServer interface.
func (c *Client) bound(ctx context.Context, project string) (lxdclient.InstanceServer, error) {
	sdk := c.sdk
	if project != "" {
		sdk = sdk.UseProject(project)
	}
	w, ok := sdk.(interface {
		WithContext(context.Context) lxdclient.InstanceServer
	})
	if !ok {
		return nil, errors.New("lxd: the SDK connection cannot carry a context")
	}
	return w.WithContext(ctx), nil
}

// mapError maps an error of a typed SDK call to an *Error (FR-S17 (4)):
// the transport's errors keep their kind, the SDK's status errors take the
// kind of their status.
func mapError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if status, ok := api.StatusErrorMatch(err); ok {
		return statusError(status, err)
	}
	if ctx.Err() != nil {
		return &Error{Kind: KindTimeout, Err: err}
	}
	return &Error{Kind: classify(err), Err: err}
}

// statusError is the *Error of an HTTP error status and LXD's message.
func statusError(status int, err error) *Error {
	kind := StatusKind(status)
	if status == http.StatusInternalServerError && strings.Contains(err.Error(), agentNotRunning) {
		kind = KindAgentNotRunning
	}
	return &Error{Kind: kind, Status: status, Err: err}
}

// responseError is the *Error of a raw response with an error status.
func responseError(resp *RawResponse) *Error {
	env, err := resp.Envelope()
	if err != nil || env.Type != api.ErrorResponse {
		return &Error{Kind: StatusKind(resp.Status), Status: resp.Status, Err: errors.New("error response without an error envelope")}
	}
	return statusError(resp.Status, errors.New(env.Error))
}

// parseFileHeaders reads the files API's headers strictly: unlike the
// SDK's parser, a missing header is an error, not a default, and the mode
// keeps its setuid, setgid and sticky bits.
func parseFileHeaders(h http.Header) (*File, error) {
	id := func(name string) (int64, error) {
		v := h.Get(name)
		if v == "" || strings.Trim(v, "0123456789") != "" {
			return 0, fmt.Errorf("files API: header %s is not a decimal id", name)
		}
		return strconv.ParseInt(v, 10, 32)
	}
	uid, err := id("X-LXD-uid")
	if err != nil {
		return nil, err
	}
	gid, err := id("X-LXD-gid")
	if err != nil {
		return nil, err
	}
	m := h.Get("X-LXD-mode")
	if len(m) != 4 || strings.Trim(m, "01234567") != "" {
		return nil, errors.New("files API: header X-LXD-mode is not 4 octal digits")
	}
	mode, _ := strconv.ParseInt(m, 8, 32) // checked above
	typ := h.Get("X-LXD-type")
	if typ != "file" && typ != "directory" && typ != "symlink" {
		return nil, errors.New("files API: header X-LXD-type is not file, directory or symlink")
	}
	return &File{UID: uid, GID: gid, Mode: int(mode), Type: typ}, nil
}
