import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { inject, Injectable } from '@angular/core';
import { catchError, Observable, throwError } from 'rxjs';
import { PlanParams, PlanResponse, Race } from './plan';

@Injectable({ providedIn: 'root' })
export class PlanService {
  private readonly http = inject(HttpClient);

  /** Lists the preloaded race courses, sorted by name. */
  races(): Observable<Race[]> {
    return this.http.get<Race[]>('api/races');
  }

  /** Fetches a preloaded race's GPX text. */
  raceGpx(id: string): Observable<string> {
    return this.http
      .get(`api/races/${encodeURIComponent(id)}`, { responseType: 'text' })
      .pipe(
        catchError((err: HttpErrorResponse) =>
          throwError(() => new Error(`Couldn't load that race (${err.status}).`)),
        ),
      );
  }

  /** Posts the GPX and params. The URL is relative so the app works under /cheersquad/. */
  plan(gpx: File, params: PlanParams): Observable<PlanResponse> {
    const body = new FormData();
    body.append('gpx', gpx);
    body.append('params', JSON.stringify(params));
    return this.http
      .post<PlanResponse>('api/plan', body)
      .pipe(
        catchError((err: HttpErrorResponse) =>
          throwError(
            () =>
              new Error(
                err.status === 429
                  ? 'Too many plans in a short time. Wait a minute and try again.'
                  : err.status === 413
                    ? 'That GPX file is too large (the limit is 3 MB).'
                    : (err.error?.error ?? `Request failed (${err.status})`),
              ),
          ),
        ),
      );
  }
}
