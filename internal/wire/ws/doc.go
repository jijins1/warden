// Package ws porte les contrats réseau entre la CLI et le rôle `api` : cadres
// du flux WebSocket, accusés d'offset, body de `POST /runs`.
//
// Le format exact du protocole (framing, ack) est une question encore ouverte
// au §14 de la spec.
//
// Types et (dé)sérialisation uniquement : ni client, ni serveur.
package ws
