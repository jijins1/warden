-- Clôture d'un run par le runner lui-même (§6.4, marche 2).
--
-- La machine à états du §6.4 énumère quatre marches, et la deuxième est celle-ci :
-- « le runner rapporte lui-même s'il le peut → cas normal, le plus riche ». Elle
-- n'avait jusqu'ici aucun chemin d'écriture : `CloseRun` existait, et aucun code
-- de production ne l'appelait. Tout run restait `running` indéfiniment.
--
-- Le chemin passe par l'ingestion, qui est la seule surface montante du pod
-- (ADR-0008). Or le rôle `warden_ingest` n'a que `INSERT` sur les tables d'events.
-- Cette migration lui donne de quoi clore un run, et rien de plus.
--
-- ÉCART ASSUMÉ à la lettre de l'ADR-0008, qui écrivait « son propre rôle
-- PostgreSQL restreint à INSERT sur la table d'events ». Le motif de cette
-- restriction — que l'ingestion ne puisse pas devenir un second chemin d'écriture
-- sur l'audit — est intact : cinq colonnes de statut, aucune sur l'identité du
-- run, son utilisateur, son commit ou ses events déjà écrits.
--
-- Ce qui borne réellement un pod à SON run n'est d'ailleurs pas ce GRANT : c'est
-- que le `run_id` est résolu côté serveur depuis le jeton présenté, et jamais
-- accepté depuis le corps d'une requête (ADR-0021, ADR-0023). Le GRANT est la
-- seconde couche, celle qui limite les dégâts si la première cédait.

-- +goose Up

-- Cinq colonnes, pas la table. Le rôle d'ingestion n'a aucune raison de pouvoir
-- réécrire `audit_sha`, `user_id` ou `mainline_sha` : ce sont les colonnes qui
-- disent QUI a exécuté QUOI, et un pod hostile qui pourrait les toucher
-- réécrirait l'audit dont il est l'objet.
--
-- `started_at` n'y est pas non plus, délibérément : il est posé au démarrage par
-- le control plane, et le laisser réécrire permettrait d'antidater un run.
GRANT UPDATE (state, termination_cause, ended_at,
              secrets_unmasked, secrets_unmasked_reason) ON run TO warden_ingest;

-- Le SELECT de `run_day` existe depuis 00001 et sert la clé de partition. Il
-- faut y ajouter `ended_at`, et c'est une conséquence directe de PostgreSQL :
-- la clause WHERE de la clôture LIT cette colonne, et lire exige le SELECT même
-- quand l'instruction est un UPDATE. Sans lui, la clôture échoue sur
-- « permission denied for table run », message qui ne nomme pas la colonne.
--
-- Cette lecture porte l'idempotence. La clôture doit pouvoir se rejouer — le
-- runner réémet son dernier lot sans danger après une coupure (ADR-0008) — et
-- une seconde clôture ne doit pas redater la fin, sans quoi la durée mesurée
-- d'un run dériverait à chaque reprise réseau.
GRANT SELECT (ended_at) ON run TO warden_ingest;

-- +goose Down

REVOKE UPDATE (state, termination_cause, ended_at,
               secrets_unmasked, secrets_unmasked_reason) ON run FROM warden_ingest;
REVOKE SELECT (ended_at) ON run FROM warden_ingest;
