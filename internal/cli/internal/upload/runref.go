package upload

import (
	"time"

	"github.com/jijins1/warden/internal/runref"
)

// NewRunRef et RunRefTime délèguent à internal/runref.
//
// Le codec a migré là pour que le serveur puisse le réutiliser (ADR-0042) sans
// importer un sous-paquet de la CLI — `internal/cli/internal/…` reste borné à la
// CLI. Ces deux fonctions restent ici, sous ce nom, pour que rien d'autre dans ce
// paquet n'ait à changer.
func NewRunRef() (string, error) { return runref.New() }

func RunRefTime(ref string) (time.Time, error) { return runref.Time(ref) }
