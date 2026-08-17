-- Les clés d'accès, une par environnement (ADR-0020).
--
-- L'ADR-0020 a décidé le composant `key-inventory` et son premier adaptateur —
-- des Secrets Kubernetes — puis rien n'a été écrit : `Order.SSHKey` part vide
-- depuis, et `core/order.go` le dit en commentaire. Cette table est la moitié
-- que PostgreSQL doit tenir.
--
-- CE QU'ELLE NE CONTIENT JAMAIS : la clé privée. Elle vit dans un Secret
-- Kubernetes, que le control plane lit au moment de la livraison. C'est la même
-- règle que la migration 00003 énonce déjà pour `runner_pod` — sinon le journal
-- d'audit devient un magasin de secrets, et il est répliqué, sauvegardé et
-- jamais purgé (ADR-0013).
--
-- Ce que la table porte est donc ce qui n'est PAS sensible et qu'il faut
-- pourtant pouvoir requêter : quelle clé publique déployer sur la flotte, quelle
-- empreinte comparer, où trouver la partie privée, et depuis quand.

-- +goose Up

CREATE TABLE ssh_key (
    -- L'environnement est la clé, parce que c'est la maille que le §10.2 pose
    -- (`utilisateur → environnement → scope d'hôtes`) et que l'ADR-0020 retient
    -- pour la v1 : « l'adaptateur ignore `user` et renvoie la clé de
    -- l'environnement ».
    --
    -- La contrainte de forme n'est pas cosmétique : cette valeur devient un
    -- composant de NOM DE SECRET Kubernetes (`warden-key-<environment>`), donc
    -- un label RFC 1123. Une majuscule ou un point produirait un Secret que
    -- l'API refuse, à la livraison de l'ordre — c'est-à-dire au pire moment,
    -- très loin d'ici. Même raisonnement que `repo_name_forme` en 00005.
    environment text PRIMARY KEY
        CONSTRAINT ssh_key_environment_forme
            CHECK (environment ~ '^[a-z0-9][a-z0-9-]{0,54}$'),

    -- Ce qu'un opérateur doit poser dans `authorized_keys`. C'est la seule
    -- moitié de la paire qui a vocation à circuler, et `warden key show` ne sert
    -- qu'à l'afficher.
    public_key text NOT NULL
        CONSTRAINT ssh_key_public_forme
            CHECK (public_key ~ '^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp[0-9]+) '),

    -- L'empreinte, sous la forme qu'affichent `ssh-keygen -l` et `ssh-add -l` :
    -- c'est elle qu'un humain compare, et la stocker sous un autre format
    -- obligerait à convertir au moment précis où l'on veut comparer.
    fingerprint text NOT NULL
        CONSTRAINT ssh_key_fingerprint_forme
            CHECK (fingerprint ~ '^SHA256:'),

    -- Où vit la partie privée. Un POINTEUR, jamais la valeur.
    --
    -- Stocké plutôt que recalculé depuis `environment` : le jour où la
    -- convention de nommage change, les lignes existantes continuent de
    -- désigner le bon Secret. Une convention recalculée est une convention qu'on
    -- ne peut plus faire évoluer sans réécrire l'existant.
    secret_name text NOT NULL
        CONSTRAINT ssh_key_secret_forme
            CHECK (secret_name ~ '^[a-z0-9][a-z0-9.-]{0,251}$'),

    -- `generated` : warden a frappé la paire, la partie privée n'a jamais quitté
    -- le cluster. `imported` : elle vient d'un opérateur, donc elle a existé
    -- ailleurs. La distinction compte le jour où l'on se demande jusqu'où une
    -- compromission remonte.
    origin text NOT NULL
        CONSTRAINT ssh_key_origin CHECK (origin IN ('generated', 'imported')),

    created_at timestamptz NOT NULL DEFAULT now(),

    -- L'ADR-0020 pose que « la rotation est le seul mécanisme de révocation »,
    -- et note qu'elle n'est pas outillée. Cette colonne ne l'outille pas
    -- davantage — elle rend seulement répondable la question « depuis quand
    -- cette clé est-elle en place », qui est la première qu'on pose en incident.
    rotated_at timestamptz,

    -- Qui a posé cette clé. Une clé d'environnement décide de ce qui peut
    -- joindre la production : savoir qui l'a changée est du même ordre que
    -- savoir qui a lancé un run.
    created_by bigint REFERENCES users (id)
);

COMMENT ON TABLE ssh_key IS
    'Partie PUBLIQUE et métadonnées des clés d''accès (ADR-0020). La partie privée vit dans un Secret Kubernetes ; cette table ne la porte jamais.';
COMMENT ON COLUMN ssh_key.secret_name IS
    'Nom du Secret Kubernetes portant la partie privée. Un pointeur, jamais la valeur.';
COMMENT ON COLUMN ssh_key.origin IS
    'generated : la partie privée n''a jamais quitté le cluster. imported : elle a existé ailleurs.';

-- GRANT.
--
-- `warden_reader` voit la table entière : rien n'y est sensible, et l'interface
-- d'audit doit pouvoir dire avec quelle clé un run a tourné.
--
-- RIEN pour `warden_ingest` : l'ingestion écrit des events, elle n'a aucune
-- raison de connaître les clés d'accès de la flotte.
GRANT SELECT ON ssh_key TO warden_reader;

-- +goose Down

DROP TABLE IF EXISTS ssh_key;
