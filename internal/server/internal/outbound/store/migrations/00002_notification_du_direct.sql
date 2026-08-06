-- Notifications du suivi temps réel (ADR-0008).
--
-- LISTEN / NOTIFY est une OPTIMISATION, pas le mécanisme de correction. La
-- correction vient de `seq` : un réplica qui ne recevrait jamais de
-- notification finit par tout servir correctement s'il relit périodiquement.
-- Ce qui suit ne fait que supprimer la latence de ce poll, lequel reste en
-- filet.
--
-- Conséquence à ne pas perdre de vue : rien ici n'est critique. Si ces triggers
-- disparaissaient, le produit serait plus lent, pas faux.
--
-- +goose Up

-- ---------------------------------------------------------------------------
-- Nouveaux events.
--
-- La notification ne porte que (run_id, seq), JAMAIS l'event. Trois raisons qui
-- tiennent chacune seule : la charge utile de NOTIFY est plafonnée (8 ko) ; le
-- réplica doit relire la table de toute façon pour ne jamais livrer ce qui
-- n'est pas journalisé (§6.3) ; et les payloads --diff vivent dans une table à
-- contrôle d'accès distinct, or les diffuser par un canal de notification
-- annulerait cette séparation.
--
-- Un canal UNIQUE pour tous les runs, filtré par le réplica. Un canal par run
-- épuiserait le mécanisme, que l'ADR-0008 note déjà comme mal adapté à un très
-- grand nombre de canaux.
--
-- Trigger STATEMENT, comme le compteur `changed` : une notification par lot
-- inséré. NOTIFY est transactionnel — il part au COMMIT, jamais avant, donc un
-- réplica n'est jamais réveillé pour des lignes qu'il ne verrait pas encore.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION warden_notify_events() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public
AS $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT run_id, max(seq) AS seq FROM new_events GROUP BY run_id
    LOOP
        PERFORM pg_notify('warden_events', r.run_id || ':' || r.seq);
    END LOOP;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER event_notify
    AFTER INSERT ON event
    REFERENCING NEW TABLE AS new_events
    FOR EACH STATEMENT
    EXECUTE FUNCTION warden_notify_events();

-- ---------------------------------------------------------------------------
-- Fin de run.
--
-- Sans ce second canal, un client resterait suspendu jusqu'au prochain poll
-- APRÈS le dernier event du run — un temps mort visible à la fin de chaque run,
-- là précisément où l'utilisateur attend le verdict.
--
-- La condition est `ended_at`, et NON un statut terminal : un run naît
-- `interrupted` / `unknown`, avant même que le pod existe (§6.4). Se fier au
-- statut ferait clore le flux à la seconde où il s'ouvre.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION warden_notify_run_ended() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public
AS $$
BEGIN
    PERFORM pg_notify('warden_run_ended', NEW.id::text);
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER run_notify_ended
    AFTER UPDATE OF ended_at ON run
    FOR EACH ROW
    WHEN (OLD.ended_at IS NULL AND NEW.ended_at IS NOT NULL)
    EXECUTE FUNCTION warden_notify_run_ended();

-- +goose Down

DROP TRIGGER IF EXISTS run_notify_ended ON run;
DROP TRIGGER IF EXISTS event_notify ON event;
DROP FUNCTION IF EXISTS warden_notify_run_ended();
DROP FUNCTION IF EXISTS warden_notify_events();
