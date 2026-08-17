-- L'inventaire des machines enrôlées.
--
-- Le §10.2 pose le modèle `utilisateur → environnement → scope d'hôtes`, dont
-- rien n'existait : `run.environment` était une chaîne sans rien derrière, et
-- aucune table ne disait quelles machines un environnement contient ni avec quel
-- compte les joindre.
--
-- CE QUE CETTE TABLE N'EST PAS : une source d'inventaire pour Ansible. Ce qui
-- décide de ce qui s'exécute est l'inventaire du commit d'audit, résolu depuis
-- l'arbre (ADR-0009) — le dupliquer ici en ferait une seconde vérité, et la
-- première divergence se paierait sur une machine de production. Ce que la table
-- porte est ce que WARDEN doit savoir : qu'une machine a été enrôlée, comment on
-- l'a jointe, et sur quelle empreinte.

-- +goose Up

CREATE TABLE managed_host (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    -- Le nom TEL QUE L'INVENTAIRE ANSIBLE LE NOMME. C'est par lui qu'on
    -- rapproche un event de run — dont l'`host` vient d'Ansible — d'une machine
    -- enrôlée. Un identifiant à nous ne se rapprocherait de rien.
    name text NOT NULL
        CONSTRAINT managed_host_name_forme
            CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$'),

    environment text NOT NULL
        CONSTRAINT managed_host_environment_forme
            CHECK (environment ~ '^[a-z0-9][a-z0-9-]{0,54}$'),

    -- `inet` et non `text`, et ce n'est pas de la coquetterie de type : PostgreSQL
    -- refuse ce qui n'est pas une adresse, ce qui supprime une validation à
    -- écrire à la main et à oublier sur le second chemin d'écriture.
    --
    -- Une ADRESSE et pas un nom : c'est ici que l'enrôlement se connecte, et
    -- dépendre d'une résolution DNS faite dans le pod ferait enrôler une machine
    -- que l'opérateur croyait désigner. Une machine enrôlée à la mauvaise adresse
    -- est une machine dont on croit détenir l'accès.
    address inet NOT NULL,

    port int NOT NULL DEFAULT 22
        CONSTRAINT managed_host_port CHECK (port BETWEEN 1 AND 65535),

    -- Le compte que warden ouvre. Ce n'est pas l'utilisateur du produit :
    -- l'ADR-0020 note déjà l'écart — « l'audit interne dira alice, les logs de
    -- l'hôte diront ansible ».
    login text NOT NULL
        CONSTRAINT managed_host_login_forme
            CHECK (login ~ '^[a-z_][a-z0-9_-]{0,31}$'),

    -- L'empreinte de la machine, ÉPINGLÉE, au format d'une ligne
    -- `known_hosts` sans le nom d'hôte. NULL tant qu'on ne l'a jamais contactée.
    --
    -- C'est la seule protection contre un intermédiaire : sans elle, se
    -- connecter revient à faire confiance à ce qui répond. Un écart ultérieur
    -- doit faire échouer, jamais avertir — un avertissement dans un pod n'est lu
    -- par personne.
    host_key text
        CONSTRAINT managed_host_key_forme
            CHECK (host_key IS NULL OR host_key ~ '^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp[0-9]+) '),

    -- `declared` : l'opérateur a posé la clé publique lui-même, warden n'a fait
    -- qu'enregistrer. `assisted` : warden s'est connecté et l'a posée.
    --
    -- La distinction n'est pas cosmétique : dans le second cas le produit a vu
    -- passer un mot de passe d'utilisateur, dans le premier non.
    enrollment_mode text NOT NULL
        CONSTRAINT managed_host_mode
            CHECK (enrollment_mode IN ('declared', 'assisted')),

    enrolled_at timestamptz NOT NULL DEFAULT now(),
    enrolled_by bigint REFERENCES users (id),

    -- Un nom par environnement. Deux `db-prod-02` dans `prod` seraient deux
    -- réponses à la même question, et le rapprochement avec les events de run —
    -- qui ne portent qu'un nom — deviendrait ambigu.
    CONSTRAINT managed_host_unique UNIQUE (environment, name)
);

-- La question posée en incident est « que sais-je de cette machine », pas
-- « que contient cet environnement ». L'index suit l'usage.
CREATE INDEX managed_host_par_nom ON managed_host (name);

COMMENT ON TABLE managed_host IS
    'Machines enrôlées : comment warden les joint et sur quelle empreinte. PAS une source d''inventaire Ansible — celui-ci vient du commit d''audit (ADR-0009).';
COMMENT ON COLUMN managed_host.address IS
    'Où l''enrôlement se connecte. Une adresse et pas un nom : dépendre du DNS du pod ferait enrôler une machine que l''opérateur croyait désigner.';
COMMENT ON COLUMN managed_host.host_key IS
    'Empreinte épinglée. NULL tant que la machine n''a jamais été contactée. Un écart ultérieur doit faire ÉCHOUER.';

-- GRANT.
--
-- `warden_reader` voit la table : l'interface d'audit doit pouvoir dire ce
-- qu'une machine touchée par un run est, et rien ici n'est sensible — une
-- empreinte publique et une adresse.
--
-- RIEN pour `warden_ingest`, comme pour `ssh_key` : l'ingestion écrit des
-- events, elle n'a aucune raison de connaître la flotte.
GRANT SELECT ON managed_host TO warden_reader;

-- +goose Down

DROP TABLE IF EXISTS managed_host;
