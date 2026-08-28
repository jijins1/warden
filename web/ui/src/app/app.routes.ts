import { Routes } from '@angular/router';
import { authGuard } from './core/auth/auth.guard';

export const routes: Routes = [
  {
    path: 'login',
    title: 'Sign in — warden',
    loadComponent: () => import('./features/auth/login').then((m) => m.Login),
  },
  {
    // La coquille porte la garde, donc TOUT ce qui vit dessous en hérite. Une
    // garde par route laisserait la prochaine s'ajouter sans, et l'oubli ne se
    // verrait pas.
    path: '',
    canActivate: [authGuard],
    loadComponent: () => import('./layout/shell').then((m) => m.Shell),
    children: [
      {
        path: 'account',
        title: 'Account — warden',
        loadComponent: () => import('./features/account/account').then((m) => m.Account),
      },
      { path: '', pathMatch: 'full', redirectTo: 'account' },
    ],
  },
  { path: '**', redirectTo: '' },
];
