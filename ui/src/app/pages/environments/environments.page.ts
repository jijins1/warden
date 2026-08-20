import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { MatCardModule } from '@angular/material/card';
import { MatTableModule, MatTableDataSource } from '@angular/material/table';
import { MatProgressSpinnerModule } from '@angular/material/progress-spinner';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatDividerModule } from '@angular/material/divider';
import { RouterModule } from '@angular/router';

import { EnvironmentsService } from '../../services/environments.service';
import { Environment } from '../../models/environment.model';

@Component({
  selector: 'app-environments-page',
  standalone: true,
  imports: [
    CommonModule,
    MatCardModule,
    MatTableModule,
    MatProgressSpinnerModule,
    MatButtonModule,
    MatIconModule,
    MatDividerModule,
    RouterModule
  ],
  template: `
    <div class="environments-page">
      <h1 class="mat-h1">Environnements</h1>

      <div *ngIf="loading" class="loading-spinner">
        <mat-spinner diameter="50"></mat-spinner>
      </div>

      <div *ngIf="!loading" class="content">
        <mat-card>
          <mat-card-content>
            <div *ngIf="environments.length === 0" class="empty-state">
              <mat-icon>folder_open</mat-icon>
              <p>Aucun environnement configuré</p>
            </div>

            <div *ngIf="environments.length > 0" class="environments-list">
              <div *ngFor="let env of environments" class="environment-item">
                <div class="environment-header">
                  <h2 class="environment-name">{{ env.name }}</h2>
                  <span class="environment-count">{{ env.runCount }} exécutions</span>
                </div>
                <div class="environment-meta">
                  <span class="environment-description">{{ env.description || 'Aucune description' }}</span>
                  <span class="environment-date">Créé: {{ formatDate(env.createdAt) }}</span>
                </div>
                <div class="environment-actions">
                  <button mat-button color="primary" [routerLink]="['/runs']" [queryParams]="{ env: env.name }">
                    <mat-icon>list</mat-icon>
                    Voir les exécutions
                  </button>
                </div>
                <mat-divider></mat-divider>
              </div>
            </div>
          </mat-card-content>
        </mat-card>
      </div>
    </div>
  `,
  styles: [`
    .environments-page {
      max-width: 1200px;
      margin: 0 auto;
    }

    .loading-spinner {
      display: flex;
      justify-content: center;
      align-items: center;
      height: 200px;
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

    .environments-list {
      display: flex;
      flex-direction: column;
      gap: 8px;
    }

    .environment-item {
      padding: 16px 0;
    }

    .environment-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 8px;
    }

    .environment-name {
      margin: 0;
      font-size: 1.2rem;
      font-weight: 500;
      color: #1565c0;
    }

    .environment-count {
      padding: 4px 12px;
      background-color: #e3f2fd;
      border-radius: 12px;
      font-size: 0.875rem;
      color: #1565c0;
    }

    .environment-meta {
      display: flex;
      justify-content: space-between;
      margin-bottom: 12px;
      font-size: 0.9rem;
      color: rgba(0, 0, 0, 0.6);
    }

    .environment-actions {
      display: flex;
      gap: 8px;
    }

    mat-divider {
      margin: 12px 0;
    }
  `]
})
export class EnvironmentsPageComponent implements OnInit {
  environments: Environment[] = [];
  loading = true;

  constructor(private environmentsService: EnvironmentsService) {}

  ngOnInit(): void {
    this.loadData();
  }

  loadData(): void {
    this.loading = true;

    this.environmentsService.listEnvironments().subscribe({
      next: (environments) => {
        this.environments = environments.sort((a, b) => a.name.localeCompare(b.name));
        this.loading = false;
      },
      error: () => {
        this.loading = false;
      }
    });
  }

  formatDate(dateString: string): string {
    const date = new Date(dateString);
    return date.toLocaleString('fr-FR', {
      year: 'numeric',
      month: 'short',
      day: 'numeric'
    });
  }
}
