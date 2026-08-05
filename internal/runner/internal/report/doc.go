// Package report envoie les events vers l'endpoint d'ingestion : lots courts,
// plafonnés en nombre et en délai, offsets assignés localement (ADR-0008).
//
// En cas d'échec il retente ; au-delà d'un seuil, LE RUN EST ARRÊTÉ. Un run dont
// l'audit ne s'écrit pas ne doit pas continuer en aveugle (§6.3).
package report
