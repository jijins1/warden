-- La clé Ansible Vault, une par environnement (ADR-0045).
--
-- Même décomposition que la migration 00007 pour les clés SSH : deux magasins
-- pour un secret, parce qu'un seul obligerait à choisir entre un journal
-- d'audit qui contient des secrets et une configuration qu'on ne peut pas
-- requêter (ADR-0013).
--
-- CE QU'ELLE NE CONTIENT JAMAIS : le mot de passe en clair. Il vit dans un
-- Secret Kubernetes, que le control plane lit au moment de la livraison de
-- l'ordre, exactement comme la clé SSH de l'ADR-0020.
--
-- Ce que la table porte est donc ce qui n'est PAS sensible et qu'il faut
-- pourtant pouvoir requêter : quelle empreinte comparer, où trouver la valeur
-- en clair, et depuis quand.

-- +goose Up

CREATE TABLE vault_key (
    -- L'environnement est la clé, même granularité que ssh_key (ADR-0045) :
    -- pas de vault-id nommé en v1, extensible plus tard sans casser le modèle.
    --
    -- Même contrainte de forme que ssh_key : cette valeur devient un
    -- composant de NOM DE SECRET Kubernetes (`warden-vault-<environment>`).
    environment text PRIMARY KEY
        CONSTRAINT vault_key_environment_forme
            CHECK (environment ~ '^[a-z0-9][a-z0-9-]{0,54}$'),

    -- L'empreinte du mot de passe (SHA-256 hex), pour l'identifier sans le
    -- révéler. Pas de format `SHA256:` comme pour ssh_key : ce n'est pas une
    -- empreinte de clé publique OpenSSH, juste un hash de comparaison.
    fingerprint text NOT NULL
        CONSTRAINT vault_key_fingerprint_forme
            CHECK (fingerprint ~ '^[0-9a-f]{64}$'),

    -- Où vit la valeur en clair. Un POINTEUR, jamais la valeur — même
    -- raisonnement que ssh_key.secret_name.
    secret_name text NOT NULL
        CONSTRAINT vault_key_secret_forme
            CHECK (secret_name ~ '^[a-z0-9][a-z0-9.-]{0,251}$'),

    -- `generated` : warden a tiré le mot de passe, il n'a jamais quitté le
    -- cluster. `imported` : il vient d'un opérateur, migration d'un dépôt
    -- déjà chiffré ailleurs.
    origin text NOT NULL
        CONSTRAINT vault_key_origin CHECK (origin IN ('generated', 'imported')),

    created_at timestamptz NOT NULL DEFAULT now(),

    -- Comme pour ssh_key : ne rend pas la rotation outillée, dit seulement
    -- depuis quand la clé courante est en place. Rotation du contenu du dépôt
    -- NON outillée — voir ADR-0045, dette assumée : remplacer cette clé
    -- invalide le déchiffrement des secrets déjà chiffrés avec l'ancienne,
    -- tant qu'ils n'ont pas été ré-encodés via `warden vault encrypt`.
    rotated_at timestamptz,

    -- Qui a posé cette clé. Une clé de vault décide de ce qui est
    -- déchiffrable dans les playbooks d'un environnement : savoir qui l'a
    -- changée est du même ordre que savoir qui a posé une clé SSH.
    created_by bigint REFERENCES users (id)
);

COMMENT ON TABLE vault_key IS
    'Métadonnées des clés Ansible Vault (ADR-0045). Le mot de passe en clair vit dans un Secret Kubernetes ; cette table ne le porte jamais.';
COMMENT ON COLUMN vault_key.secret_name IS
    'Nom du Secret Kubernetes portant le mot de passe en clair. Un pointeur, jamais la valeur.';
COMMENT ON COLUMN vault_key.origin IS
    'generated : le mot de passe n''a jamais quitté le cluster. imported : il a existé ailleurs.';

-- GRANT, mêmes raisons que ssh_key : rien n'est sensible ici, `warden_reader`
-- voit la table entière ; `warden_ingest` n'a aucune raison de la connaître.
GRANT SELECT ON vault_key TO warden_reader;

-- +goose Down

DROP TABLE IF EXISTS vault_key;
