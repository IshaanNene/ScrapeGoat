/**
 * ScrapeGoat TypeScript SDK — Typed client for the ScrapeGoat REST API.
 *
 * @example
 * ```ts
 * import { ScrapeGoat } from 'scrapegoat-sdk';
 *
 * const sg = new ScrapeGoat({ baseUrl: 'http://localhost:8080', apiKey: 'sk-...' });
 * const result = await sg.crawl({ url: 'https://example.com', depth: 2 });
 * console.log(result.items);
 * ```
 */

// --- Types ---

export type JobStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';

export interface CrawlOptions {
  url: string;
  depth?: number;
  concurrency?: number;
  selectors?: Record<string, string>;
  allowedDomains?: string[];
  maxRequests?: number;
  respectRobots?: boolean;
  userAgent?: string;
  /** If true, poll until job completes. Default: true */
  wait?: boolean;
  /** Seconds between status polls. Default: 2 */
  pollInterval?: number;
}

export interface CrawlResult {
  jobId: string;
  status: JobStatus;
  items: Record<string, unknown>[];
  itemsCount: number;
  pagesCrawled: number;
  durationMs: number;
  errors: string[];
}

export interface ExtractOptions {
  url: string;
  schema: Record<string, unknown>;
  model?: string;
}

export interface ExtractResult {
  url: string;
  data: Record<string, unknown>;
  model: string;
  tokensUsed: number;
  cached: boolean;
  costUsd: number;
}

export interface SEOAuditResult {
  url: string;
  score: number;
  issues: Record<string, unknown>[];
  meta: Record<string, unknown>;
}

export interface Job {
  id: string;
  status: JobStatus;
  url: string;
  createdAt?: string;
  startedAt?: string;
  completedAt?: string;
  error?: string;
  itemsCount: number;
  pagesCrawled: number;
}

export interface ScrapeGoatConfig {
  baseUrl?: string;
  apiKey?: string;
  timeout?: number;
}

// --- Errors ---

export class ScrapeGoatError extends Error {
  statusCode: number;
  response?: unknown;

  constructor(message: string, statusCode: number = 0, response?: unknown) {
    super(message);
    this.name = 'ScrapeGoatError';
    this.statusCode = statusCode;
    this.response = response;
  }
}

export class AuthenticationError extends ScrapeGoatError {
  constructor(message: string) {
    super(message, 401);
    this.name = 'AuthenticationError';
  }
}

export class NotFoundError extends ScrapeGoatError {
  constructor(message: string) {
    super(message, 404);
    this.name = 'NotFoundError';
  }
}

export class RateLimitError extends ScrapeGoatError {
  retryAfter: number;

  constructor(message: string, retryAfter: number = 0) {
    super(message, 429);
    this.name = 'RateLimitError';
    this.retryAfter = retryAfter;
  }
}

// --- Client ---

export class ScrapeGoat {
  private baseUrl: string;
  private headers: Record<string, string>;
  private timeout: number;

  constructor(config: ScrapeGoatConfig = {}) {
    this.baseUrl = (config.baseUrl ?? 'http://localhost:8080').replace(/\/$/, '');
    this.timeout = config.timeout ?? 300_000;
    this.headers = { 'Content-Type': 'application/json' };
    if (config.apiKey) {
      this.headers['Authorization'] = `Bearer ${config.apiKey}`;
    }
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeout);

    try {
      const resp = await fetch(`${this.baseUrl}${path}`, {
        method,
        headers: this.headers,
        body: body ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });

      if (!resp.ok) {
        const text = await resp.text();
        if (resp.status === 401) throw new AuthenticationError(text);
        if (resp.status === 404) throw new NotFoundError(text);
        if (resp.status === 429) {
          const retryAfter = parseFloat(resp.headers.get('Retry-After') ?? '5');
          throw new RateLimitError(text, retryAfter);
        }
        throw new ScrapeGoatError(`API error (${resp.status}): ${text}`, resp.status);
      }

      return (await resp.json()) as T;
    } finally {
      clearTimeout(timer);
    }
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  // --- Health ---

  async health(): Promise<Record<string, unknown>> {
    return this.request('GET', '/health');
  }

  // --- Crawl ---

  async crawl(options: CrawlOptions): Promise<CrawlResult> {
    const {
      url,
      depth = 3,
      concurrency = 10,
      selectors = {},
      allowedDomains = [],
      maxRequests = 0,
      respectRobots = true,
      userAgent,
      wait = true,
      pollInterval = 2,
    } = options;

    const body = {
      url,
      depth,
      concurrency,
      selectors,
      allowed_domains: allowedDomains,
      max_requests: maxRequests,
      respect_robots: respectRobots,
      ...(userAgent ? { user_agent: userAgent } : {}),
    };

    const data = await this.request<{ job_id?: string; id?: string }>('POST', '/api/v1/crawl', body);
    const jobId = data.job_id ?? data.id ?? '';

    if (!wait) {
      return {
        jobId,
        status: 'pending',
        items: [],
        itemsCount: 0,
        pagesCrawled: 0,
        durationMs: 0,
        errors: [],
      };
    }

    // Poll for completion.
    let job: Job;
    do {
      await this.sleep(pollInterval * 1000);
      job = await this.getJob(jobId);
    } while (job.status === 'pending' || job.status === 'running');

    // Fetch results.
    const results = await this.request<{
      items?: Record<string, unknown>[];
      items_count?: number;
      pages_crawled?: number;
      duration_ms?: number;
      errors?: string[];
    }>('GET', `/api/v1/jobs/${jobId}/results`);

    return {
      jobId,
      status: job.status,
      items: results.items ?? [],
      itemsCount: results.items_count ?? (results.items?.length ?? 0),
      pagesCrawled: results.pages_crawled ?? 0,
      durationMs: results.duration_ms ?? 0,
      errors: results.errors ?? [],
    };
  }

  // --- Extract ---

  async extract(options: ExtractOptions): Promise<ExtractResult> {
    const body = {
      url: options.url,
      schema: options.schema,
      ...(options.model ? { model: options.model } : {}),
    };

    const data = await this.request<{
      data?: Record<string, unknown>;
      model?: string;
      tokens_used?: number;
      cached?: boolean;
      cost_usd?: number;
    }>('POST', '/api/v1/extract', body);

    return {
      url: options.url,
      data: data.data ?? {},
      model: data.model ?? '',
      tokensUsed: data.tokens_used ?? 0,
      cached: data.cached ?? false,
      costUsd: data.cost_usd ?? 0,
    };
  }

  // --- SEO ---

  async seoAudit(url: string): Promise<SEOAuditResult> {
    const data = await this.request<{
      score?: number;
      issues?: Record<string, unknown>[];
      meta?: Record<string, unknown>;
    }>('POST', '/api/v1/seo/audit', { url });

    return {
      url,
      score: data.score ?? 0,
      issues: data.issues ?? [],
      meta: data.meta ?? {},
    };
  }

  // --- Jobs ---

  async getJob(jobId: string): Promise<Job> {
    const data = await this.request<Record<string, unknown>>('GET', `/api/v1/jobs/${jobId}`);
    return {
      id: (data.id as string) ?? '',
      status: (data.status as JobStatus) ?? 'pending',
      url: (data.url as string) ?? '',
      createdAt: data.created_at as string | undefined,
      startedAt: data.started_at as string | undefined,
      completedAt: data.completed_at as string | undefined,
      error: data.error as string | undefined,
      itemsCount: (data.items_count as number) ?? 0,
      pagesCrawled: (data.pages_crawled as number) ?? 0,
    };
  }

  async listJobs(limit: number = 50): Promise<Job[]> {
    const data = await this.request<{ jobs?: Record<string, unknown>[] }>(
      'GET',
      `/api/v1/jobs?limit=${limit}`
    );
    const jobs = data.jobs ?? [];
    return jobs.map((j) => ({
      id: (j.id as string) ?? '',
      status: (j.status as JobStatus) ?? 'pending',
      url: (j.url as string) ?? '',
      itemsCount: (j.items_count as number) ?? 0,
      pagesCrawled: (j.pages_crawled as number) ?? 0,
    }));
  }

  async cancelJob(jobId: string): Promise<Job> {
    const data = await this.request<Record<string, unknown>>('DELETE', `/api/v1/jobs/${jobId}`);
    return {
      id: (data.id as string) ?? '',
      status: (data.status as JobStatus) ?? 'cancelled',
      url: (data.url as string) ?? '',
      itemsCount: (data.items_count as number) ?? 0,
      pagesCrawled: (data.pages_crawled as number) ?? 0,
    };
  }
}

export default ScrapeGoat;
