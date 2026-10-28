// Package version porte la version du binaire, posée à la compilation.
//
// Partagé par la CLI et le control plane, et c'est la seule chose qu'ils
// partagent ici : une chaîne. Aucun type de fil, aucune règle — l'ADR-0011
// tient toujours.
//
// La valeur est injectée par l'éditeur de liens :
//
//	go build -ldflags "-X github.com/jijins1/warden/internal/version.Version=0.0.42"
//
// La CI le fait pour les releases et les images (ADR-0049). Un build qui ne le
// fait pas — `make install`, `make images` — reste en `Dev`.
package version

// Dev est la version d'un binaire compilé sans version.
const Dev = "dev"

// Version est la version de ce binaire, sans le `v` du tag.
var Version = Dev

// EstDev dit si `v` désigne un build de développement.
//
// La chaîne vide compte comme tel : un serveur antérieur à l'ADR-0049 n'annonce
// rien, et ne rien annoncer ne doit pas déclencher de mise à jour.
func EstDev(v string) bool { return v == "" || v == Dev }
