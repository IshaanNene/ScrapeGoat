"""Sync and async HTTP clients for the ScrapeGoat API."""

from __future__ import annotations

import time
from typing import Any, Optional

import httpx

from scrapegoat.models import (
    CrawlRequest,
    CrawlResult,
    ExtractRequest,
    ExtractResult,
    Job,
    JobStatus,
    SEOAuditResult,
)


class ScrapeGoatError(Exception):
    """Base exception for ScrapeGoat SDK."""

    def __init__(self, message: str, status_code: int = 0, response: Any = None):
        super().__init__(message)
        self.status_code = status_code
        self.response = response


class AuthenticationError(ScrapeGoatError):
    """Raised when authentication fails."""


class NotFoundError(ScrapeGoatError):
    """Raised when a resource is not found."""


class RateLimitError(ScrapeGoatError):
    """Raised when rate limited."""

    def __init__(self, message: str, retry_after: float = 0, **kwargs: Any):
        super().__init__(message, **kwargs)
        self.retry_after = retry_after


def _raise_for_status(resp: httpx.Response) -> None:
    """Raise typed exceptions for HTTP error codes."""
    if resp.is_success:
        return
    body = resp.text
    if resp.status_code == 401:
        raise AuthenticationError(f"Authentication failed: {body}", status_code=401)
    if resp.status_code == 404:
        raise NotFoundError(f"Not found: {body}", status_code=404)
    if resp.status_code == 429:
        retry_after = float(resp.headers.get("Retry-After", "5"))
        raise RateLimitError(f"Rate limited: {body}", retry_after=retry_after, status_code=429)
    raise ScrapeGoatError(f"API error ({resp.status_code}): {body}", status_code=resp.status_code)


class ScrapeGoat:
    """Synchronous client for the ScrapeGoat API.

    Usage:
        sg = ScrapeGoat("http://localhost:8080", api_key="sk-...")
        result = sg.crawl("https://example.com", depth=2)
        for item in result.items:
            print(item)
    """

    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        api_key: Optional[str] = None,
        timeout: float = 300.0,
    ):
        headers: dict[str, str] = {"Content-Type": "application/json"}
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"

        self._client = httpx.Client(
            base_url=base_url.rstrip("/"),
            headers=headers,
            timeout=timeout,
        )

    def close(self) -> None:
        """Close the underlying HTTP client."""
        self._client.close()

    def __enter__(self) -> "ScrapeGoat":
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    # --- Health ---

    def health(self) -> dict[str, Any]:
        """Check API server health."""
        resp = self._client.get("/health")
        _raise_for_status(resp)
        return resp.json()

    # --- Crawl ---

    def crawl(
        self,
        url: str,
        depth: int = 3,
        concurrency: int = 10,
        selectors: Optional[dict[str, str]] = None,
        allowed_domains: Optional[list[str]] = None,
        max_requests: int = 0,
        respect_robots: bool = True,
        user_agent: Optional[str] = None,
        wait: bool = True,
        poll_interval: float = 2.0,
    ) -> CrawlResult:
        """Submit a crawl job and optionally wait for completion.

        Args:
            url: Seed URL to crawl.
            depth: Maximum crawl depth.
            concurrency: Number of concurrent workers.
            selectors: CSS selectors for extraction.
            allowed_domains: Restrict crawl to these domains.
            max_requests: Max requests (0 = unlimited).
            respect_robots: Respect robots.txt.
            user_agent: Custom User-Agent.
            wait: If True, poll until job completes.
            poll_interval: Seconds between status polls.

        Returns:
            CrawlResult with items and metadata.
        """
        req = CrawlRequest(
            url=url,
            depth=depth,
            concurrency=concurrency,
            selectors=selectors or {},
            allowed_domains=allowed_domains or [],
            max_requests=max_requests,
            respect_robots=respect_robots,
            user_agent=user_agent,
        )

        resp = self._client.post("/api/v1/crawl", json=req.model_dump(exclude_none=True))
        _raise_for_status(resp)
        data = resp.json()
        job_id = data.get("job_id", data.get("id", ""))

        if not wait:
            return CrawlResult(job_id=job_id, status=JobStatus.PENDING)

        # Poll for completion.
        while True:
            job = self.get_job(job_id)
            if job.status in (JobStatus.COMPLETED, JobStatus.FAILED, JobStatus.CANCELLED):
                break
            time.sleep(poll_interval)

        # Fetch results.
        resp = self._client.get(f"/api/v1/jobs/{job_id}/results")
        _raise_for_status(resp)
        result_data = resp.json()

        return CrawlResult(
            job_id=job_id,
            status=job.status,
            items=result_data.get("items", []),
            items_count=result_data.get("items_count", len(result_data.get("items", []))),
            pages_crawled=result_data.get("pages_crawled", 0),
            duration_ms=result_data.get("duration_ms", 0),
            errors=result_data.get("errors", []),
        )

    # --- Extract ---

    def extract(
        self,
        url: str,
        schema: dict[str, Any],
        model: Optional[str] = None,
    ) -> ExtractResult:
        """Extract structured data from a URL using LLM.

        Args:
            url: URL to extract from.
            schema: JSON schema defining fields to extract.
            model: LLM model to use (e.g., gpt-4o, claude-3-5-sonnet).

        Returns:
            ExtractResult with extracted data.
        """
        req = ExtractRequest(url=url, schema=schema, model=model)
        resp = self._client.post("/api/v1/extract", json=req.model_dump(exclude_none=True))
        _raise_for_status(resp)
        data = resp.json()

        return ExtractResult(
            url=url,
            data=data.get("data", {}),
            model=data.get("model", ""),
            tokens_used=data.get("tokens_used", 0),
            cached=data.get("cached", False),
            cost_usd=data.get("cost_usd", 0.0),
        )

    # --- SEO ---

    def seo_audit(self, url: str) -> SEOAuditResult:
        """Run an SEO audit on a URL.

        Args:
            url: URL to audit.

        Returns:
            SEOAuditResult with score and issues.
        """
        resp = self._client.post("/api/v1/seo/audit", json={"url": url})
        _raise_for_status(resp)
        data = resp.json()

        return SEOAuditResult(
            url=url,
            score=data.get("score", 0),
            issues=data.get("issues", []),
            meta=data.get("meta", {}),
        )

    # --- Jobs ---

    def get_job(self, job_id: str) -> Job:
        """Get the status of a job."""
        resp = self._client.get(f"/api/v1/jobs/{job_id}")
        _raise_for_status(resp)
        return Job.model_validate(resp.json())

    def list_jobs(self, limit: int = 50) -> list[Job]:
        """List recent jobs."""
        resp = self._client.get("/api/v1/jobs", params={"limit": limit})
        _raise_for_status(resp)
        data = resp.json()
        return [Job.model_validate(j) for j in data.get("jobs", data if isinstance(data, list) else [])]

    def cancel_job(self, job_id: str) -> Job:
        """Cancel a running job."""
        resp = self._client.delete(f"/api/v1/jobs/{job_id}")
        _raise_for_status(resp)
        return Job.model_validate(resp.json())


class AsyncScrapeGoat:
    """Async client for the ScrapeGoat API.

    Usage:
        async with AsyncScrapeGoat("http://localhost:8080", api_key="sk-...") as sg:
            result = await sg.crawl("https://example.com")
    """

    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        api_key: Optional[str] = None,
        timeout: float = 300.0,
    ):
        headers: dict[str, str] = {"Content-Type": "application/json"}
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"

        self._client = httpx.AsyncClient(
            base_url=base_url.rstrip("/"),
            headers=headers,
            timeout=timeout,
        )

    async def close(self) -> None:
        await self._client.aclose()

    async def __aenter__(self) -> "AsyncScrapeGoat":
        return self

    async def __aexit__(self, *args: Any) -> None:
        await self.close()

    async def health(self) -> dict[str, Any]:
        resp = await self._client.get("/health")
        _raise_for_status(resp)
        return resp.json()

    async def crawl(self, url: str, **kwargs: Any) -> CrawlResult:
        """Async crawl — submits and returns immediately (no polling)."""
        req = CrawlRequest(url=url, **kwargs)
        resp = await self._client.post("/api/v1/crawl", json=req.model_dump(exclude_none=True))
        _raise_for_status(resp)
        data = resp.json()
        return CrawlResult(
            job_id=data.get("job_id", data.get("id", "")),
            status=JobStatus.PENDING,
        )

    async def extract(self, url: str, schema: dict[str, Any], model: Optional[str] = None) -> ExtractResult:
        req = ExtractRequest(url=url, schema=schema, model=model)
        resp = await self._client.post("/api/v1/extract", json=req.model_dump(exclude_none=True))
        _raise_for_status(resp)
        data = resp.json()
        return ExtractResult(
            url=url,
            data=data.get("data", {}),
            model=data.get("model", ""),
            tokens_used=data.get("tokens_used", 0),
            cached=data.get("cached", False),
            cost_usd=data.get("cost_usd", 0.0),
        )

    async def get_job(self, job_id: str) -> Job:
        resp = await self._client.get(f"/api/v1/jobs/{job_id}")
        _raise_for_status(resp)
        return Job.model_validate(resp.json())

    async def list_jobs(self, limit: int = 50) -> list[Job]:
        resp = await self._client.get("/api/v1/jobs", params={"limit": limit})
        _raise_for_status(resp)
        data = resp.json()
        return [Job.model_validate(j) for j in data.get("jobs", data if isinstance(data, list) else [])]
