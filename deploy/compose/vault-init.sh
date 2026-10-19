#!/bin/sh
# Configure le Vault de développement comme l'ADR-0047 le décrit.
#
# Idempotent : relancé sur un Vault déjà configuré, il ne fait que
# renouveler le secret_id.
set -eu

monter() {
	vault secrets list -format=json | grep -q "\"$1/\"" ||
		vault secrets enable -path="$1" -version=2 kv
}

# La consigne : une seule version par clé, détruite à la lecture.
monter warden-transit
vault write warden-transit/config max_versions=1 >/dev/null
# Les clés d'accès et les clés de vault.
monter warden

vault auth list -format=json | grep -q '"approle/"' || vault auth enable approle

# Pas de `list` sur la consigne : un serveur compromis ne peut pas énumérer
# les valeurs en attente. La seule suppression permise est la destruction.
vault policy write warden - <<'EOF'
path "warden-transit/data/*"     { capabilities = ["create", "update", "read"] }
path "warden-transit/metadata/*" { capabilities = ["delete"] }
path "warden/data/*"             { capabilities = ["create", "update", "read"] }
EOF

vault write auth/approle/role/warden token_policies=warden token_ttl=1h token_max_ttl=24h >/dev/null
# Un role_id fixe : il n'est pas secret, et le connaître d'avance évite de le
# transmettre au serveur par un autre canal que sa configuration.
vault write auth/approle/role/warden/role-id role_id=warden >/dev/null

umask 077
vault write -f -field=secret_id auth/approle/role/warden/secret-id >/vault-auth/secret_id
# Lisible par l'utilisateur du serveur (65532) : en développement, le volume
# n'est monté que chez lui.
chmod 0444 /vault-auth/secret_id
echo "vault configuré"
