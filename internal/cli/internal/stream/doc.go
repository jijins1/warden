// Package stream est le client du flux d'events : connexion WS, replay depuis
// l'offset acquitté, detach et reattach (§9.1).
//
// Exigence de sortie de la phase 1 (§12) : un run de dix minutes survit à trois
// coupures réseau client et à un redémarrage du serveur, sans perte d'event.
package stream
