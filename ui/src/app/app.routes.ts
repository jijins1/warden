import { Routes } from '@angular/router';
import { DashboardPageComponent } from './pages/dashboard/dashboard.page';
import { RunsPageComponent } from './pages/runs/runs.page';
import { RunDetailPageComponent } from './pages/run-detail/run-detail.page';
import { HostsPageComponent } from './pages/hosts/hosts.page';
import { HostDetailPageComponent } from './pages/host-detail/host-detail.page';
import { EnvironmentsPageComponent } from './pages/environments/environments.page';

export const appRoutes: Routes = [
  {
    path: '',
    component: DashboardPageComponent,
    title: 'Tableau de bord - Warden'
  },
  {
    path: 'runs',
    component: RunsPageComponent,
    title: 'Exécutions - Warden'
  },
  {
    path: 'runs/:runRef',
    component: RunDetailPageComponent,
    title: 'Détails de l\'exécution - Warden'
  },
  {
    path: 'hosts',
    component: HostsPageComponent,
    title: 'Machines - Warden'
  },
  {
    path: 'hosts/:host',
    component: HostDetailPageComponent,
    title: 'Exécutions par machine - Warden'
  },
  {
    path: 'environments',
    component: EnvironmentsPageComponent,
    title: 'Environnements - Warden'
  },
  {
    path: '**',
    redirectTo: '',
    pathMatch: 'full'
  }
];
