import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { MatCardModule } from '@angular/material/card';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatDividerModule } from '@angular/material/divider';
import { ActivatedRoute, RouterModule } from '@angular/router';

import { HostsService } from '../../services/hosts.service';
import { RunsService } from '../../services/runs.service';
import { RunListItem } from '../../models/run.model';

@Component({
  selector: 'app-host-detail-page',
  standalone: true,
  imports: [
    CommonModule,
    MatCardModule,
    MatButtonModule,
    MatIconModule,
    MatProgressSpinnerModule,
    MatDividerModule,
    RouterModule
  ],
  template: `
    <div class="host-detail-page">
      <h1 class="mat-h1">Exécutions pour {{ host }}</h1>

      <div *ngIf="loading" class="loading-spinner">
        <mat-spinner diameter="50"></mat-spinner>
      </div>

      <div *ngIf="!loading" class="content">
        <!-- Host Info -->
        <mat-card class="host-info-card">
          <mat-card-content>
            <h2>Informations sur la machine</h2>
            <mat-divider></mat-divider>
            <div class="host-info-grid">
              <div class="info-item">
                <span class="label">Nom:</span>
                <span class="value">{{ host }}</span>
              </div>
              <div class="info-item">
                <span class="label">Environnement:</span>
                <span class="value">{{ hostInfo?.environment || '-' }}</span>
              </div>
              <div class="info-item">
                <span class="label">Adresse:</span>
                <span class="value">{{ hostInfo?.address || '-' }}</span>
              </div>
              <div class="info-item">
                <span class="label">Utilisateur:</span>
                <span class="value">{{ hostInfo?.login || '-' }}</span>
              </div>
              <div class="info-item">
                <span class="label">Enrôlée le:</span>
                <span class="value">{{ hostInfo?.enrolledAt ? formatDate(hostInfo.enrolledAt) : '-' }}</span>
              </div>
              <div class="info-item">
                <span class="label">Nombre d'exécutions:</span>
                <span class="value">{{ runs.length }}</span>
              </div>
            </div>
          </mat-card-content>
        </mat-card>

        <!-- Runs List -->
        <mat-card class="runs-card">
          <mat-card-header>
            <mat-card-title>Exécutions sur cette machine</mat-card-title>
          </mat-card-header>
          <mat-card-content>
            <div *ngIf="runsLoading" class="runs-loading">
              <mat-spinner diameter="30"></mat-spinner>
            </div>

            <div *ngIf="!runsLoading && runs.length === 0" class="empty-runs">
              <mat-icon>inbox</mat-icon>
              <p>Aucune exécution trouvée pour cette machine</p>
            </div>

            <div *ngIf="runs.length > 0" class="runs-list">
              <div *ngFor="let run of runs" class="run-item" [routerLink]="['/runs', run.runRef]">
                <div class="run-header">
                  <span class="run-ref">{{ run.runRef }}</span>
                  <span [class]="'status-badge ' + run.state">{{ run.state }}</span>
                </div>
                <div class="run-body">
                  <span class="run-playbook">{{ run.playbook }}</span>
                  <span class="run-environment">{{ run.environment }} / {{ run.inventory }}</span>
                </div>
                <div class="run-footer">
                  <span class="run-date">{{ formatDate(run.startedAt) }}</span>
                  <span class="run-changed">{{ run.changedCount }} changements</span>
                </div>
              </div>
            </div>
          </mat-card-content>
        </mat-card>

        <!-- Back Button -->
        <div class="back-button">
          <button mat-button color="primary" routerLink="/hosts">
            <mat-icon>arrow_back</mat-icon>
            Retour à la liste
          </button>
        </div>
      </div>

      <div *ngIf="!loading && !hostInfo" class="not-found">
        <mat-icon>error_outline</mat-icon>
        <h2>Machine non trouvée</h2>
        <p>La machine "{{ host }}" n'existe pas ou a été supprimée.</p>
        <button mat-button color="primary" routerLink="/hosts">Retour à la liste</button>
      </div>
    </div>
  `,
  styles: [`
    .host-detail-page {
      max-width: 1400px;
      margin: 0 auto;
    }

    .loading-spinner {
      display: flex;
      justify-content: center;
      align-items: center;
      height: 200px;
    }

    .host-info-card {
      margin-bottom: 20px;
    }

    .host-info-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
      gap: 16px;
      margin-top: 16px;
    }

    .info-item {
      display: flex;
      justify-content: space-between;
      padding: 8px 0;
      border-bottom: 1px solid #e0e0e0;
    }

    .label {
      font-weight: 500;
      color: rgba(0, 0, 0, 0.6);
    }

    .value {
      color: #333;
    }

    .runs-card {
      margin-bottom: 20px;
    }

    .runs-loading {
      display: flex;
      justify-content: center;
      padding: 20px;
    }

    .empty-runs {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 40px;
      color: rgba(0, 0, 0, 0.6);

      mat-icon {
        font-size: 48px;
        margin-bottom: 16px;
      }
    }

    .runs-list {
      display: flex;
      flex-direction: column;
      gap: 12px;
    }

    .run-item {
      padding: 12px;
      border-radius: 4px;
      cursor: pointer;
      transition: background-color 0.2s;

      &:hover {
        background-color: #f5f5f5;
      }
    }

    .run-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 8px;
    }

    .run-ref {
      font-weight: 500;
      color: #1565c0;
    }

    .status-badge {
      padding: 4px 12px;
      border-radius: 12px;
      font-size: 0.75rem;
      font-weight: 500;
      text-transform: uppercase;

      &.pending, &.running {
        background-color: #fff3e0;
        color: #f57c00;
      }
      &.succeeded {
        background-color: #e8f5e9;
        color: #2e7d32;
      }
      &.failed {
        background-color: #ffebee;
        color: #c62828;
      }
      &.cancelled {
        background-color: #fff3e0;
        color: #f57c00;
      }
    }

    .run-body {
      display: flex;
      flex-direction: column;
      gap: 4px;
      margin-bottom: 8px;
    }

    .run-playbook {
      font-weight: 500;
    }

    .run-environment {
      font-size: 0.875rem;
      color: rgba(0, 0, 0, 0.6);
    }

    .run-footer {
      display: flex;
      justify-content: space-between;
      font-size: 0.8rem;
      color: rgba(0, 0, 0, 0.5);
    }

    .back-button {
      margin-top: 20px;
    }

    .not-found {
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      padding: 40px;
      text-align: center;
      color: rgba(0, 0, 0, 0.6);

      mat-icon {
        font-size: 64px;
        margin-bottom: 16px;
      }

      h2 {
        margin: 0 0 16px;
      }

      button {
        margin-top: 16px;
      }
    }
  `]
})
export class HostDetailPageComponent implements OnInit {
  host = '';
  hostInfo: any | null = null;
  runs: RunListItem[] = [];
  loading = true;
  runsLoading = true;

  constructor(
    private route: ActivatedRoute,
    private hostsService: HostsService,
    private runsService: RunsService
  ) {}

  ngOnInit(): void {
    this.host = this.route.snapshot.paramMap.get('host') || '';
    this.loadHostInfo();
    this.loadRuns();
  }

  loadHostInfo(): void {
    this.loading = true;
    // For now, we'll get host info from the runs
    // In a real implementation, we'd use hostsService.getHost()
    this.hostsService.listHosts({ search: this.host }).subscribe({
      next: (hosts) => {
        this.hostInfo = hosts.find(h => h.host === this.host) || null;
        this.loading = false;
      },
      error: () => {
        this.loading = false;
      }
    });
  }

  loadRuns(): void {
    this.runsLoading = true;
    this.runsService.getRunsByHost(this.host).subscribe({
      next: (runs) => {
        this.runs = runs;
        this.runsLoading = false;
      },
      error: () => {
        this.runsLoading = false;
      }
    });
  }

  formatDate(dateString: string): string {
    const date = new Date(dateString);
    return date.toLocaleString('fr-FR', {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    });
  }
}
