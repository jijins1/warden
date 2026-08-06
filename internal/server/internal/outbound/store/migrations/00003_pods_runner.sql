-- Pods runner : identité d'amorçage, claim et routage du dispatch (ADR-0023).
--
-- Première table du schéma qui ne relève pas de l'audit. Elle est ici, et non
-- dans un magasin séparé, parce que les trois choses qu'elle porte sont
-- indissociables : QUI est ce pod (identité), QUEL run il exécute (claim), et
-- OÙ envoyer l'ordre (routage). Les séparer imposerait une transaction
-- distribuée à l'endroit exact où le §8.4 exige de l'atomicité.
--
-- Ce qu'elle ne contient JAMAIS : le jeton d'amorçage en clair, l'ordre de run,
-- le jeton git de l'ADR-0007, la clé d'accès de l'ADR-0020. La notification de
-- dispatch porte un pointeur et rien d'autre — sinon ces secrets seraient
-- écrits dans le WAL, et le journal d'audit deviendrait un magasin de secrets.
--
-- +goose Up

-- ---------------------------------------------------------------------------
-- runner_pod
--
-- Une ligne par pod chaud, créée par le contrôleur AVANT le pod lui-même : le
-- Secret qui porte le jeton doit exister avant que le kubelet monte le volume,
-- sans quoi le pod attend au montage — et l'attente au démarrage est ce que
-- tout le pré-chauffage cherche à supprimer (§8.1, ADR-0015).
-- ---------------------------------------------------------------------------

CREATE TABLE runner_pod (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pod_name text NOT NULL UNIQUE CHECK (pod_name <> ''),

    -- Renseigné après création, quand l'API la rend. Purement corrélatif : le
    -- nom suffit à l'identité, les noms étant générés avec un suffixe aléatoire.
    -- Sa valeur est de distinguer deux pods homonymes dans les logs
    -- d'infrastructure, le jour où ça arrive.
    pod_uid text,

    -- Empreinte SHA-256 du jeton d'amorçage, jamais le jeton (ADR-0023).
    -- L'authentification est une RECHERCHE par empreinte, pas une comparaison :
    -- aucun secret n'est comparé octet à octet, et compromettre cette base ne
    -- donne aucune identité de pod.
    token_hash bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),

    -- Clé de pool du §8.3. Un pod n'est éligible qu'aux runs du même triplet :
    -- c'est ce qui interdit qu'un pod chaud d'alice serve un run de bob, et
    -- c'est aussi ce qui rend le cache Ansible non partagé du §7 vrai par
    -- construction plutôt que par configuration.
    user_id     bigint NOT NULL REFERENCES users (id),
    repo        text   NOT NULL,
    environment text   NOT NULL,

    -- Le claim. UNIQUE : un run n'est exécuté que par un pod, et un pod n'est
    -- jamais réutilisé pour deux runs (§8.2).
    run_id     bigint UNIQUE REFERENCES run (id),
    claimed_at timestamptz,

    -- Renseigné à l'ouverture du canal de dispatch, remis à NULL quand il
    -- tombe. C'est le prédicat « connexion oisive » de l'ADR-0018 : on ne
    -- réclame que des pods effectivement joignables.
    ready_at timestamptz,

    -- Demande d'annulation (§5.6). Une colonne et non un message : le signal
    -- doit survivre au fait que le réplica qui tient la connexion ne soit pas
    -- celui qui reçoit le Ctrl-C.
    cancel_requested_at timestamptz,

    created_at  timestamptz NOT NULL DEFAULT now(),
    released_at timestamptz,

    -- Un pod réclamé porte son run, et réciproquement.
    CONSTRAINT runner_pod_claim_coherent
        CHECK ((run_id IS NULL) = (claimed_at IS NULL)),
    -- Un pod libéré n'est plus joignable. La libération doit donc effacer
    -- `ready_at` dans la même instruction — c'est ce qui empêche qu'un pod mort
    -- reste réclamable.
    CONSTRAINT runner_pod_released_not_ready
        CHECK (released_at IS NULL OR ready_at IS NULL)
);

COMMENT ON TABLE runner_pod IS
    'Identité d''amorçage et claim des pods chauds (ADR-0023). État opérationnel, pas de l''audit : c''est la seule table du schéma dans ce cas.';
COMMENT ON COLUMN runner_pod.token_hash IS
    'SHA-256 du jeton d''amorçage. Le jeton lui-même n''existe qu''à deux endroits : le Secret Kubernetes du pod, et la mémoire du pod.';
COMMENT ON COLUMN runner_pod.released_at IS
    'Définitif (§8.2 : jamais réutilisé). C''est ce qui dispense ce secret de la procédure de rotation dont dépendent les ADR-0017 et 0020.';
COMMENT ON COLUMN runner_pod.ready_at IS
    'Peut mentir si un réplica meurt brutalement : les pods qu''il tenait restent marqués prêts. Dette nommée à l''ADR-0023.';

-- L'index du claim. Il porte exactement le prédicat de la requête de claim, et
-- partiel parce que la table accumule les pods morts sans jamais les purger
-- (ADR-0013) : sans le filtre, l'index grossirait indéfiniment pour servir une
-- recherche qui ne regarde que les vivants.
CREATE INDEX runner_pod_libres_idx
    ON runner_pod (user_id, repo, environment, created_at)
    WHERE run_id IS NULL AND released_at IS NULL AND ready_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Routage du dispatch.
--
-- Même mécanisme que le flux d'events (ADR-0008), en sens inverse : le claim
-- est gagné par un réplica quelconque, la connexion SSE du pod est tenue par un
-- autre. UN canal pour tous les pods, filtré par le réplica — un canal par pod
-- épuiserait le mécanisme.
--
-- La charge utile est l'id du pod, et rien de plus. Le réplica qui détient la
-- connexion relit l'état et va chercher les credentials lui-même : c'est ce qui
-- garantit qu'aucun credential n'est persisté, à aucun moment.
--
-- Un seul trigger pour le claim ET l'annulation, parce que le réplica relit de
-- toute façon : la notification ne dit pas QUOI a changé, elle dit qu'il faut
-- regarder. Comme pour les events, elle n'est qu'une optimisation — le poll du
-- réplica sert le même résultat, plus lentement.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION warden_notify_pod_dispatch() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public
AS $$
BEGIN
    PERFORM pg_notify('warden_pod_dispatch', NEW.id::text);
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER runner_pod_notify_dispatch
    AFTER UPDATE OF run_id, cancel_requested_at ON runner_pod
    FOR EACH ROW
    WHEN ((OLD.run_id IS NULL AND NEW.run_id IS NOT NULL)
       OR (OLD.cancel_requested_at IS NULL AND NEW.cancel_requested_at IS NOT NULL))
    EXECUTE FUNCTION warden_notify_pod_dispatch();

-- ---------------------------------------------------------------------------
-- GRANT.
--
-- L'ingestion résout `pod -> run` depuis le jeton présenté (ADR-0023) : c'est
-- ce qui rend enfin vrai l'énoncé sur lequel repose le schéma de l'ADR-0021,
-- « le lien run_id <-> pod est établi côté serveur et jamais accepté depuis le
-- corps d'une requête ».
--
-- Quatre colonnes, pas la table : le rôle d'ingestion n'a aucune raison de
-- connaître la clé de pool, donc de savoir quel utilisateur possède quel pod.
-- ---------------------------------------------------------------------------

GRANT SELECT (id, run_id, token_hash, released_at) ON runner_pod TO warden_ingest;

-- +goose Down

DROP TRIGGER IF EXISTS runner_pod_notify_dispatch ON runner_pod;
DROP FUNCTION IF EXISTS warden_notify_pod_dispatch();
DROP TABLE IF EXISTS runner_pod;
