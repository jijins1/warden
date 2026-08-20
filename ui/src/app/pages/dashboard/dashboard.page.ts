import { Component, OnInit, signal, computed, effect } from '@angular/core';
import { CommonModule } from '@angular/common';
import { MatCardModule } from '@angular/material/card';
import { MatGridListModule } from '@angular/material/grid-list';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatButtonModule } from '@angular/material/button';
import { RouterModule } from '@angular/router';

import { DashboardService } from '../../services/dashboard.service';
import { RunsService } from '../../services/runs.service';
import { RunListItem, RunState, RunSummary } from '../../models/run.model';

@Component({
  selector: 'app-dashboard-page',
  standalone: true,
  imports: [
    CommonModule,
    MatCardModule,
    MatGridListModule,
    MatIconModule,
    MatProgressSpinnerModule,
    MatButtonModule,
    RouterModule
  ],
  template: `
    <div class="dashboard">
      <h1 class="mat-h1">Tableau de bord</h1>
      
      <div *ngIf="loading()" class="loading-spinner">
        <mat-spinner diameter="50"></mat-spinner>
      </div>

      <div *ngIf="!loading()" class="content">
        <!-- Summary Cards -->
        <div class="summary-cards">
          <mat-card class="summary-card">
            <mat-card-content>
              <div class="card-icon total">
                <mat-icon>play_arrow</mat-icon>
              </div>
              <div class="card-content">
                <span class="card-value">{{ summary()?.totalRuns || 0 }}</span>
                <span class="card-label">Exécutions totales</span>
              </div>
            </mat-card-content>
          </mat-card>

          <mat-card class="summary-card">
            <mat-card-content>
              <div class="card-icon success">
                <mat-icon>check_circle</mat-icon>
              </div>
              <div class="card-content">
                <span class="card-value">{{ summary()?.succeeded || 0 }}</span>
                <span class="card-label">Succès</span>
              </div>
            </mat-card-content>
          </mat-card>

          <mat-card class="summary-card">
            <mat-card-content>
              <div class="card-icon failed">
                <mat-icon>error</mat-icon>
              </div>
              <div class="card-content">
                <span class="card-value">{{ summary()?.failed || 0 }}</span>
                <span class="card-label">Échecs</span>
              </div>
            </mat-card-content>
          </mat-card>

          <mat-card class="summary-card">
            <mat-card-content>
              <div class="card-icon running">
                <mat-icon>sync</mat-icon>
              </div>
              <div class="card-content">
                <span class="card-value">{{ summary()?.running || 0 }}</span>
                <span class="card-label">En cours</span>
              </div>
            </mat-card-content>
          </mat-card>

          <mat-card class="summary-card">
            <mat-card-content>
              <div class="card-icon changes">
                <mat-icon>swap_horiz</mat-icon>
              </div>
              <div class="card-content">
                <span class="card-value">{{ summary()?.totalChanged || 0 }}</span>
                <span class="card-label">Changements</span>
              </div>
            </mat-card-content>
          </mat-card>
        </div>

        <!-- Recent Runs -->
        <mat-card class="recent-runs-card">
          <mat-card-header>
            <mat-card-title>Exécutions récentes</mat-card-title>
            <button mat-button color="primary" routerLink="/runs">
              Voir tout
            </button>
          </mat-card-header>
          <mat-card-content>
            <div *ngIf="recentRuns().length === 0" class="empty-state">
              <mat-icon>inbox</mat-icon>
              <p>Aucune exécution récente</p>
            </div>
            <div *ngIf="recentRuns().length > 0" class="recent-runs-list">
              <div *ngFor="let run of recentRuns()" class="run-item" [routerLink]="['/runs', run.runRef]">
                <div class="run-info">
                  <span class="run-ref">{{ run.runRef }}</span>
                  <span class="run-playbook">{{ run.playbook }}</span>
                  <span class="run-environment">{{ run.environment }}</span>
                </div>
                <div class="run-status">
                  <span [class]="'status-badge ' + run.state">{{ run.state }}</span>
                  <span class="run-date">{{ formatDate(run.startedAt) }}</span>
                </div>
              </div>
            </div>
          </mat-card-content>
        </mat-card>

        <!-- By Environment -->
        <mat-card class="stats-card" *ngIf="summary()?.byEnvironment">
          <mat-card-header>
            <mat-card-title>Par environnement</mat-card-title>
          </mat-card-header>
          <mat-card-content>
            <div class="env-stats">
              <div *ngFor="let env of getEnvironmentKeys()" class="env-stat">
                <span class="env-name">{{ env }}</span>
                <span class="env-count">{{ summary()?.byEnvironment[env] }}</span>
              </div>
            </div>
          </mat-card-content>
        </mat-card>
      </div>
    </div>
  `,
  styles: [`
    .dashboard {
      max-width: 1400px;
      margin: 0 auto;
    }

    .loading-spinner {
      display: flex;
      justify-content: center;
      align-items: center;
      height: 200px;
    }

    .summary-cards {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
      gap: 20px;
      margin-bottom: 30px;
    }

    .summary-card {
      min-height: 120px;
      display: flex;
      align-items: center;
      justify-content: center;
      cursor: pointer;
      transition: transform 0.2s, box-shadow 0.2s;

      &:hover {
        transform: translateY(-4px);
        box-shadow: 0 6px 12px rgba(0, 0, 0, 0.15);
      }
    }

    .summary-card mat-card-content {
      display: flex;
      align-items: center;
      gap: 16px;
      width: 100%;
      padding: 16px;
    }

    .card-icon {
      width: 48px;
      height: 48px;
      border-radius: 50%;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 24px;

      &.total {
        background-color: #e3f2fd;
        color: #1565c0;
      }
      &.success {
        background-color: #e8f5e9;
        color: #2e7d32;
      }
      &.failed {
        background-color: #ffebee;
        color: #c62828;
      }
      &.running {
        background-color: #fff3e0;
        color: #f57c00;
      }
      &.changes {
        background-color: #f3e5f5;
        color: #7b1fa2;
      }
    }

    .card-content {
      display: flex;
      flex-direction: column;
    }

    .card-value {
      font-size: 2rem;
      font-weight: 500;
      line-height: 1;
    }

    .card-label {
      font-size: 0.875rem;
      color: rgba(0, 0, 0, 0.6);
      margin-top: 4px;
    }

    .recent-runs-card, .stats-card {
      margin-bottom: 30px;
    }

    .recent-runs-card mat-card-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
    }

    .empty-state {
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

    .recent-runs-list {
      display: flex;
      flex-direction: column;
      gap: 12px;
    }

    .run-item {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 12px;
      border-radius: 4px;
      cursor: pointer;
      transition: background-color 0.2s;

      &:hover {
        background-color: #f5f5f5;
      }
    }

    .run-info {
      display: flex;
      flex-direction: column;
      gap: 4px;
    }

    .run-ref {
      font-weight: 500;
      color: #1565c0;
    }

    .run-playbook, .run-environment {
      font-size: 0.875rem;
      color: rgba(0, 0, 0, 0.6);
    }

    .run-status {
      display: flex;
      flex-direction: column;
      align-items: flex-end;
      gap: 4px;
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

    .run-date {
      font-size: 0.75rem;
      color: rgba(0, 0, 0, 0.5);
    }

    .env-stats {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
      gap: 16px;
    }

    .env-stat {
      display: flex;
      justify-content: space-between;
      padding: 8px 16px;
      background-color: #f5f5f5;
      border-radius: 4px;
    }

    .env-name {
      font-weight: 500;
    }

    .env-count {
      color: #1565c0;
    }
  `]
})
export class DashboardPageComponent implements OnInit {
  // Signals for state
  private readonly _loading = signal<boolean>(true);
  private readonly _summary = signal<RunSummary | null>(null);
  private readonly _recentRuns = signal<RunListItem[]>([]);

  // Public readonly signals
  readonly loading = this._loading.asReadonly();
  readonly summary = this._summary.asReadonly();
  readonly recentRuns = this._recentRuns.asReadonly();

  constructor(
    private dashboardService: DashboardService,
    private runsService: RunsService
  ) {}

  ngOnInit(): void {
    this.loadData();
  }

  loadData(): void {
    this._loading.set(true);

    // Load summary
    this.dashboardService.getSummary().subscribe({
      next: (summary) => {
        this._summary.set(summary);
        this._loading.set(false);
      },
      error: () => {
        this._loading.set(false);
      }
    });

    // Load recent runs
    this.runsService.listRuns({ limit: 10 }).subscribe({
      next: (result) => {
        this._recentRuns.set(result.runs);
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

  getEnvironmentKeys(): string[] {
    const summary = this._summary();
    if (!summary?.byEnvironment) {
      return [];
    }
    return Object.keys(summary.byEnvironment).sort();
  }
}
