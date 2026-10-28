package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// SaveHost écrit ou remplace une machine de l'inventaire.
//
// Un UPSERT sur `(environment, name)` : redéclarer une machine est le geste
// normal — on corrige une adresse, on change de compte, on réenrôle après une
// réinstallation. Le refuser obligerait à supprimer d'abord, donc à passer par
// un état où la machine n'est plus connue.
//
// `enrolled_at` N'EST PAS redaté au remplacement : la question posée en incident
// est « depuis quand cette machine est-elle dans le périmètre », pas « quand
// a-t-on corrigé sa fiche ».
func (s *Store) SaveHost(ctx context.Context, h core.ManagedHost) error {
	if err := h.Validate(); err != nil {
		return err
	}

	const q = `
INSERT INTO managed_host (name, environment, address, port, login, host_key,
                          enrollment_mode, enrolled_by)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, NULLIF($8, 0)::bigint)
ON CONFLICT (environment, name) DO UPDATE
   SET address         = EXCLUDED.address,
       port            = EXCLUDED.port,
       login           = EXCLUDED.login,
       host_key        = EXCLUDED.host_key,
       enrollment_mode = EXCLUDED.enrollment_mode,
       enrolled_by     = EXCLUDED.enrolled_by`

	_, err := s.pool.Exec(ctx, q, h.Name, h.Environment, h.Address,
		h.Port, h.Login, h.HostKey, string(h.Mode), h.EnrolledBy)
	if err != nil {
		return fmt.Errorf("enregistrement de la machine %s/%s: %w", h.Environment, h.Name, err)
	}
	return nil
}

// HostByName rend une machine de l'inventaire.
func (s *Store) HostByName(ctx context.Context, environment, name string) (core.ManagedHost, error) {
	const q = `
SELECT name, environment, address, port, login, coalesce(host_key, ''),
       enrollment_mode, enrolled_at, coalesce(enrolled_by, 0)
  FROM managed_host
 WHERE environment = $1 AND name = $2`

	h, err := scanHost(s.pool.QueryRow(ctx, q, environment, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ManagedHost{}, fmt.Errorf("%s/%s: %w", environment, name, core.ErrNoHost)
	}
	if err != nil {
		return core.ManagedHost{}, fmt.Errorf("lecture de la machine %s/%s: %w", environment, name, err)
	}
	return h, nil
}

// ListHosts rend les machines d'un environnement, ou toutes.
//
// Un seul chemin SQL pour les deux cas plutôt que deux requêtes : la clause
// `$1 = ”` est lue une fois et deux requêtes jumelles finissent par diverger
// sur la liste des colonnes.
func (s *Store) ListHosts(ctx context.Context, environment string) ([]core.ManagedHost, error) {
	const q = `
SELECT name, environment, address, port, login, coalesce(host_key, ''),
       enrollment_mode, enrolled_at, coalesce(enrolled_by, 0)
  FROM managed_host
 WHERE $1 = '' OR environment = $1
 ORDER BY environment, name`

	lignes, err := s.pool.Query(ctx, q, environment)
	if err != nil {
		return nil, fmt.Errorf("liste des machines: %w", err)
	}
	defer lignes.Close()

	var out []core.ManagedHost
	for lignes.Next() {
		h, err := scanHost(lignes)
		if err != nil {
			return nil, fmt.Errorf("liste des machines: %w", err)
		}
		out = append(out, h)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des machines: %w", err)
	}
	return out, nil
}

// Bornes de la page de ListerMachines, les mêmes que celles de ListRuns.
const (
	machinesParPageParDefaut = 50
	machinesParPageAuPlus    = 500
)

// ListerMachines rend une page de machines bornée par une portée.
//
// Pagination par keyset sur `(environment, name)`, l'ordre de la contrainte
// `managed_host_unique` : la page suivante part de l'index au lieu de
// parcourir les lignes déjà servies. Une ligne SONDE en plus dit s'il y a une
// suite sans un COUNT.
//
// Une portée vide rend une page vide SANS interroger la base — mais APRÈS
// avoir relu le curseur : un curseur illisible reste une faute de l'appelant,
// quels que soient ses droits.
func (s *Store) ListerMachines(ctx context.Context, f core.FiltreDeMachines) (core.PageDeMachines, error) {
	if f.Limit <= 0 {
		f.Limit = machinesParPageParDefaut
	}
	if f.Limit > machinesParPageAuPlus {
		f.Limit = machinesParPageAuPlus
	}

	var apres *positionDeMachine
	if f.Cursor != "" {
		p, err := decoderCurseurDeMachine(f.Cursor)
		if err != nil {
			return core.PageDeMachines{}, fmt.Errorf("%w %q: %w", core.ErrCurseurIllisible, f.Cursor, err)
		}
		apres = &p
	}

	page := core.PageDeMachines{Hosts: []core.ManagedHost{}}
	if len(f.Environnements) == 0 {
		return page, nil
	}

	// Les paramètres optionnels sont neutralisés en SQL (`$n = ''`) plutôt
	// qu'assemblés en Go : une seule requête, lisible d'un bloc, dont la liste
	// de colonnes ne peut pas diverger d'une variante à l'autre.
	const q = `
SELECT name, environment, address, port, login, coalesce(host_key, ''),
       enrollment_mode, enrolled_at, coalesce(enrolled_by, 0)
  FROM managed_host
 WHERE environment = ANY($1)
   AND ($2 = '' OR name ILIKE '%' || $2 || '%')
   AND ($3 = '' OR (environment, name) > ($3, $4))
 ORDER BY environment, name
 LIMIT $5`

	var envCurseur, nomCurseur string
	if apres != nil {
		envCurseur, nomCurseur = apres.Environment, apres.Name
	}

	lignes, err := s.pool.Query(ctx, q, f.Environnements, echapperLike(f.Nom),
		envCurseur, nomCurseur, f.Limit+1)
	if err != nil {
		return core.PageDeMachines{}, fmt.Errorf("liste des machines visibles: %w", err)
	}
	defer lignes.Close()

	for lignes.Next() {
		h, err := scanHost(lignes)
		if err != nil {
			return core.PageDeMachines{}, fmt.Errorf("liste des machines visibles: %w", err)
		}
		page.Hosts = append(page.Hosts, h)
	}
	if err := lignes.Err(); err != nil {
		return core.PageDeMachines{}, fmt.Errorf("liste des machines visibles: %w", err)
	}

	if len(page.Hosts) > f.Limit {
		page.Hosts = page.Hosts[:f.Limit]
		dernier := page.Hosts[f.Limit-1]
		page.NextCursor = encoderCurseurDeMachine(positionDeMachine{
			Environment: dernier.Environment, Name: dernier.Name,
		})
	}
	return page, nil
}

// positionDeMachine est la clé keyset d'une machine.
//
// Encodée en JSON puis en base64url plutôt que jointe par un séparateur : le
// curseur reste opaque et relisible quel que soit le caractère que contient un
// nom, aujourd'hui borné par une contrainte, demain peut-être pas.
type positionDeMachine struct {
	Environment string `json:"e"`
	Name        string `json:"n"`
}

func encoderCurseurDeMachine(p positionDeMachine) string {
	brut, _ := json.Marshal(p) // deux chaînes : l'encodage ne peut pas échouer
	return base64.RawURLEncoding.EncodeToString(brut)
}

func decoderCurseurDeMachine(curseur string) (positionDeMachine, error) {
	brut, err := base64.RawURLEncoding.DecodeString(curseur)
	if err != nil {
		return positionDeMachine{}, errors.New("not base64url")
	}
	var p positionDeMachine
	if err := json.Unmarshal(brut, &p); err != nil {
		return positionDeMachine{}, errors.New("not a host position")
	}
	// Un environnement vide neutraliserait la clause keyset et ramènerait à la
	// première page : la boucle infinie qu'un curseur illisible doit éviter.
	if p.Environment == "" || p.Name == "" {
		return positionDeMachine{}, errors.New("incomplete host position")
	}
	return p, nil
}

// echapperLike neutralise les jokers d'un motif ILIKE : `q=db_` cherche
// « db_ », pas « db suivi de n'importe quel caractère ». La barre oblique
// inverse est l'échappement par défaut de LIKE, elle s'échappe donc aussi.
func echapperLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ForgetHost retire une machine de l'inventaire.
//
// Elle ne retire RIEN de la machine : la clé publique reste dans son
// `authorized_keys`. L'appelant doit le dire à l'utilisateur, faute de quoi le
// produit laisserait croire à une révocation qui n'a pas eu lieu.
func (s *Store) ForgetHost(ctx context.Context, environment, name string) error {
	const q = `DELETE FROM managed_host WHERE environment = $1 AND name = $2`

	tag, err := s.pool.Exec(ctx, q, environment, name)
	if err != nil {
		return fmt.Errorf("retrait de la machine %s/%s: %w", environment, name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s/%s: %w", environment, name, core.ErrNoHost)
	}
	return nil
}

// scanHost lit une ligne de `managed_host`.
//
// `address` est lue TELLE QUELLE depuis la migration 00012 (ADR-0036). Elle
// était auparavant un `inet`, qui rendait `10.0.0.1/32` et imposait un
// `host(address)` en SQL puis un `netip.ParseAddr` ici. La colonne étant
// devenue `text` — pour accueillir un nom autant qu'une adresse —, les deux
// conversions ont disparu ensemble, et c'est la valeur écrite qui est relue.
func scanHost(l interface{ Scan(...any) error }) (core.ManagedHost, error) {
	var h core.ManagedHost
	var mode string

	if err := l.Scan(&h.Name, &h.Environment, &h.Address, &h.Port, &h.Login,
		&h.HostKey, &mode, &h.EnrolledAt, &h.EnrolledBy); err != nil {
		return core.ManagedHost{}, err
	}

	h.Mode = core.EnrollmentMode(mode)
	return h, nil
}
