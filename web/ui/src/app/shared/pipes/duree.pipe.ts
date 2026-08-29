import { Pipe, PipeTransform } from '@angular/core';

/**
 * La durée entre deux instants, lisible d'un coup d'œil.
 *
 * Une fin ABSENTE ne vaut pas zéro : le run court encore, et la durée se compte
 * jusqu'à maintenant. Rendre « 0s » sur un run en vol afficherait un fait faux.
 */
@Pipe({ name: 'duree' })
export class DureePipe implements PipeTransform {
  transform(debut: string, fin?: string | null): string {
    const t0 = Date.parse(debut);
    if (Number.isNaN(t0)) {
      return '—';
    }
    const t1 = fin ? Date.parse(fin) : Date.now();
    if (Number.isNaN(t1)) {
      return '—';
    }
    return formater(Math.max(0, t1 - t0));
  }
}

function formater(ms: number): string {
  if (ms < 1000) {
    return `${Math.round(ms)}ms`;
  }
  const s = ms / 1000;
  if (s < 60) {
    // Une décimale sous la minute : la différence entre 3,1 s et 3,9 s se voit
    // dans un profil de tâches, celle entre 3 min 01 et 3 min 09 non.
    return `${s.toFixed(1)}s`;
  }
  const minutes = Math.floor(s / 60);
  const reste = Math.round(s % 60);
  if (minutes < 60) {
    return `${minutes}m ${String(reste).padStart(2, '0')}s`;
  }
  const heures = Math.floor(minutes / 60);
  return `${heures}h ${String(minutes % 60).padStart(2, '0')}m`;
}
