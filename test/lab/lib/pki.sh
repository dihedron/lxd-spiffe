# shellcheck shell=bash
# The lab's PKI: one self-signed client certificate (ECDSA P-256) per probe
# identity, trusted by every LXD server of the lab. Keys never leave the
# state directory.

readonly LAB_IDENTITIES=(read files edit full outsider)

pki_dir() { echo "$LAB_STATE_DIR/pki"; }
identity_cert() { echo "$(pki_dir)/$1.crt"; }
identity_key() { echo "$(pki_dir)/$1.key"; }

# identity_fingerprint IDENTITY: the certificate's SHA-256 fingerprint, as LXD
# writes it.
identity_fingerprint() { lxd_fingerprint_pem <"$(identity_cert "$1")"; }

# ensure_pki: creates the identities' certificates, unless they exist.
ensure_pki() {
	local id
	mkdir -p "$(pki_dir)"
	chmod 0700 "$(pki_dir)"
	for id in "${LAB_IDENTITIES[@]}"; do
		[[ -f "$(identity_cert "$id")" && -f "$(identity_key "$id")" ]] && continue
		info "creating the client certificate of identity $id"
		(
			umask 077
			openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
				-days 730 -subj "/CN=lxd-lab-$id" \
				-addext "keyUsage=critical,digitalSignature" \
				-addext "extendedKeyUsage=clientAuth" \
				-keyout "$(identity_key "$id")" -out "$(identity_cert "$id")" 2>/dev/null
		) || die "cannot create the certificate of identity $id"
	done
}
