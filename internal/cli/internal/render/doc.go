// Package render affiche le déroulement d'un run : sortie temps réel, hôtes en
// vol (§5.8), et escalade du Ctrl-C à trois niveaux (§5.6).
//
// L'affichage des hôtes en vol est un différenciateur produit, pas un confort :
// c'est ce qu'Ansible ne montre pas et qu'on cherche pendant un incident.
package render
