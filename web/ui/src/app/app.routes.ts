import { Routes } from '@angular/router';
import { authGuard, adminGuard } from './core/auth/auth.guard';

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
        path: 'runs',
        title: 'My runs — warden',
        loadComponent: () => import('./features/runs/run-list').then((m) => m.RunList),
      },
      {
        // `runRef` est lié par `withComponentInputBinding` : c'est ce qui rend
        // un lien profond rechargeable, et la raison d'être de
        // `GET /runs/{run_ref}`.
        path: 'runs/:runRef',
        title: 'Run — warden',
        loadComponent: () => import('./features/runs/run-detail').then((m) => m.RunDetail),
      },
      {
        // Deux chemins, un composant : sans machine on saisit un nom, avec on
        // voit ses runs. Séparer en deux écrans dupliquerait le formulaire.
        path: 'hosts',
        title: 'Runs by machine — warden',
        loadComponent: () => import('./features/hosts/host-runs').then((m) => m.HostRuns),
      },
      {
        path: 'hosts/:host',
        title: 'Runs by machine — warden',
        loadComponent: () => import('./features/hosts/host-runs').then((m) => m.HostRuns),
      },
      {
        path: 'account',
        title: 'Account — warden',
        loadComponent: () => import('./features/account/account').then((m) => m.Account),
      },
      {
        // `adminGuard` en plus de la garde de la coquille : `GET /admin/repos`
        // rend une carte de l'organisation (voir HandleListRepos), pas une vue
        // restreinte à l'appelant comme le reste de l'UI.
        path: 'repos',
        title: 'Repos — warden',
        canActivate: [adminGuard],
        loadComponent: () => import('./features/repos/repo-list').then((m) => m.RepoList),
      },
      {
        // Même raisonnement que `repos` : `GET /admin/keys` est réservé aux
        // administrateurs côté serveur (ADR-0020).
        path: 'environments',
        title: 'Environments — warden',
        canActivate: [adminGuard],
        loadComponent: () =>
          import('./features/environments/environment-list').then((m) => m.EnvironmentList),
      },
      { path: '', pathMatch: 'full', redirectTo: 'runs' },
    ],
  },
  { path: '**', redirectTo: '' },
];
