//go:build integration

// Étage 2 de l'ADR-0010 : la liste paginée des machines visibles (ADR-0052)
// sur un PostgreSQL réel. Ce qui se vérifie ici ne se vérifie pas sans base :
// l'ordre de collation, la comparaison de lignes du keyset, et l'échappement
// des jokers d'ILIKE.
package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// inventaireDePagination enregistre des machines sur trois environnements, et
// rend celles de la portée dans l'ordre attendu (environnement, nom).
func inventaireDePagination(t *testing.T, ctx context.Context, s *store.Store) []string {
	t.Helper()
	var attendues []string
	for _, env := range []string{"prod", "recette", "staging"} {
		for _, nom := range []string{"db-01", "db-02", "db_x", "dbx", "web-01", "web-02", "WEB-03"} {
			if err := s.SaveHost(ctx, machine(env, nom, "10.0.0.1")); err != nil {
				t.Fatalf("SaveHost %s/%s: %v", env, nom, err)
			}
		}
	}
	toutes, err := s.ListHosts(ctx, "")
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	// L'ordre de référence est celui de ListHosts — même ORDER BY, même
	// collation — moins l'environnement hors portée.
	for _, h := range toutes {
		if h.Environment != "recette" {
			attendues = append(attendues, h.Environment+"/"+h.Name)
		}
	}
	return attendues
}

// Parcourir toutes les pages rend chaque machine de la portée UNE fois, dans
// l'ordre, et rien d'autre.
func TestLesMachinesVisiblesSePaginentSansPerteNiDoublon(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	attendues := inventaireDePagination(t, ctx, s)

	var vues []string
	curseur := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("la pagination ne termine pas")
		}
		page, err := s.ListerMachines(ctx, core.FiltreDeMachines{
			Environnements: []string{"staging", "prod"}, Limit: 3, Cursor: curseur,
		})
		if err != nil {
			t.Fatalf("ListerMachines (page %d): %v", pages, err)
		}
		if len(page.Hosts) > 3 {
			t.Fatalf("page de %d machines, limite 3", len(page.Hosts))
		}
		for _, h := range page.Hosts {
			vues = append(vues, h.Environment+"/"+h.Name)
		}
		if page.NextCursor == "" {
			break
		}
		curseur = page.NextCursor
	}

	if !slices.Equal(vues, attendues) {
		t.Errorf("parcours :\n  vu      %v\n  attendu %v", vues, attendues)
	}
}

// Une page exactement pleine n'annonce pas de suite : la ligne sonde est ce qui
// le décide, pas la taille de la page.
func TestUneDernierePagePleineNAnnoncePasDeSuite(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	for _, nom := range []string{"a", "b"} {
		if err := s.SaveHost(ctx, machine("prod", nom, "10.0.0.1")); err != nil {
			t.Fatalf("SaveHost: %v", err)
		}
	}
	page, err := s.ListerMachines(ctx, core.FiltreDeMachines{Environnements: []string{"prod"}, Limit: 2})
	if err != nil {
		t.Fatalf("ListerMachines: %v", err)
	}
	if len(page.Hosts) != 2 || page.NextCursor != "" {
		t.Errorf("%d machines, curseur %q : attendu 2 et aucun", len(page.Hosts), page.NextCursor)
	}
}

// `q` est une sous-chaîne insensible à la casse, et ses jokers ne sont PAS des
// jokers : `db_` cherche « db_ », pas « db suivi d'un caractère ».
func TestLeFiltreSurLeNomEstUneSousChaineLitterale(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	inventaireDePagination(t, ctx, s)

	for q, attendu := range map[string][]string{
		"web":  {"prod/web-01", "prod/web-02", "prod/WEB-03"},
		"WEB-": {"prod/web-01", "prod/web-02", "prod/WEB-03"},
		"db_":  {"prod/db_x"},
		"%":    {},
		"b-0":  {"prod/db-01", "prod/db-02", "prod/web-01", "prod/web-02", "prod/WEB-03"},
	} {
		page, err := s.ListerMachines(ctx, core.FiltreDeMachines{Environnements: []string{"prod"}, Nom: q})
		if err != nil {
			t.Fatalf("ListerMachines q=%q: %v", q, err)
		}
		var vues []string
		for _, h := range page.Hosts {
			vues = append(vues, h.Environment+"/"+h.Name)
		}
		slices.Sort(vues)
		slices.Sort(attendu)
		if !slices.Equal(vues, attendu) {
			t.Errorf("q=%q : %v, attendu %v", q, vues, attendu)
		}
	}
}

// La portée borne la liste : un environnement hors portée n'apparaît jamais,
// et une portée VIDE ne rend rien — jamais tout.
func TestLaPorteeBorneLaListeDesMachines(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	inventaireDePagination(t, ctx, s)

	page, err := s.ListerMachines(ctx, core.FiltreDeMachines{Environnements: []string{"recette"}, Limit: 500})
	if err != nil {
		t.Fatalf("ListerMachines: %v", err)
	}
	for _, h := range page.Hosts {
		if h.Environment != "recette" {
			t.Errorf("machine hors portée : %s/%s", h.Environment, h.Name)
		}
	}
	if len(page.Hosts) != 7 {
		t.Errorf("%d machines, attendu 7", len(page.Hosts))
	}

	for nom, portee := range map[string][]string{"nil": nil, "vide": {}, "inconnue": {"ailleurs"}} {
		page, err := s.ListerMachines(ctx, core.FiltreDeMachines{Environnements: portee})
		if err != nil {
			t.Fatalf("ListerMachines (%s): %v", nom, err)
		}
		if page.Hosts == nil || len(page.Hosts) != 0 || page.NextCursor != "" {
			t.Errorf("portée %s : %+v, attendu une page vide", nom, page)
		}
	}
}

// Un curseur illisible est une erreur nommée, pas un retour à la première page
// — même sur une portée vide, où la base n'est pas interrogée.
func TestUnCurseurDeMachineIllisibleEstRefuse(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	for _, curseur := range []string{"pas-un-curseur!", "e30", "bm9u"} {
		for _, portee := range [][]string{{"prod"}, {}} {
			_, err := s.ListerMachines(ctx, core.FiltreDeMachines{Environnements: portee, Cursor: curseur})
			if !errors.Is(err, core.ErrCurseurIllisible) {
				t.Errorf("curseur %q, portée %v : erreur = %v", curseur, portee, err)
			}
		}
	}
}
