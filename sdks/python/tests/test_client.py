"""Tests for the ScrapeGoat Python SDK using mocked HTTP."""

import json

import httpx
import pytest
import respx

from scrapegoat import ScrapeGoat, AsyncScrapeGoat, JobStatus
from scrapegoat.client import AuthenticationError, NotFoundError, RateLimitError, ScrapeGoatError


BASE_URL = "http://localhost:8080"


# --- Sync Client Tests ---


class TestScrapeGoatSync:
    """Tests for the synchronous ScrapeGoat client."""

    def test_health(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/health").respond(
                json={"status": "ok", "version": "dev", "uptime": "1h2m3s"}
            )
            with ScrapeGoat(BASE_URL) as sg:
                result = sg.health()
                assert result["status"] == "ok"

    def test_crawl_wait(self):
        with respx.mock:
            # Submit crawl.
            respx.post(f"{BASE_URL}/api/v1/crawl").respond(
                json={"job_id": "job-1", "status": "pending"}
            )
            # Poll status — first running, then completed.
            route = respx.get(f"{BASE_URL}/api/v1/jobs/job-1")
            route.side_effect = [
                httpx.Response(200, json={"id": "job-1", "status": "running", "url": "https://example.com"}),
                httpx.Response(200, json={"id": "job-1", "status": "completed", "url": "https://example.com"}),
            ]
            # Fetch results.
            respx.get(f"{BASE_URL}/api/v1/jobs/job-1/results").respond(
                json={
                    "items": [{"title": "Hello", "url": "https://example.com"}],
                    "items_count": 1,
                    "pages_crawled": 3,
                    "duration_ms": 1500,
                }
            )

            with ScrapeGoat(BASE_URL) as sg:
                result = sg.crawl("https://example.com", depth=2, poll_interval=0.01)
                assert result.job_id == "job-1"
                assert result.status == JobStatus.COMPLETED
                assert len(result.items) == 1
                assert result.items[0]["title"] == "Hello"
                assert result.pages_crawled == 3

    def test_crawl_no_wait(self):
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/crawl").respond(
                json={"job_id": "job-2", "status": "pending"}
            )
            with ScrapeGoat(BASE_URL) as sg:
                result = sg.crawl("https://example.com", wait=False)
                assert result.job_id == "job-2"
                assert result.status == JobStatus.PENDING

    def test_extract(self):
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/extract").respond(
                json={
                    "data": {"title": "Product X", "price": 29.99},
                    "model": "gpt-4o",
                    "tokens_used": 450,
                    "cached": False,
                    "cost_usd": 0.003,
                }
            )
            with ScrapeGoat(BASE_URL) as sg:
                result = sg.extract(
                    "https://example.com/product",
                    schema={"title": "string", "price": "number"},
                )
                assert result.data["title"] == "Product X"
                assert result.model == "gpt-4o"
                assert result.tokens_used == 450

    def test_seo_audit(self):
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/seo/audit").respond(
                json={
                    "score": 85,
                    "issues": [{"type": "missing_alt", "severity": "warning"}],
                    "meta": {"title": "Example", "description": "A page"},
                }
            )
            with ScrapeGoat(BASE_URL) as sg:
                result = sg.seo_audit("https://example.com")
                assert result.score == 85
                assert len(result.issues) == 1

    def test_get_job(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/jobs/job-1").respond(
                json={"id": "job-1", "status": "completed", "url": "https://x.com", "items_count": 5}
            )
            with ScrapeGoat(BASE_URL) as sg:
                job = sg.get_job("job-1")
                assert job.id == "job-1"
                assert job.status == JobStatus.COMPLETED
                assert job.items_count == 5

    def test_list_jobs(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/jobs").respond(
                json={
                    "jobs": [
                        {"id": "j1", "status": "completed", "url": "https://a.com"},
                        {"id": "j2", "status": "running", "url": "https://b.com"},
                    ]
                }
            )
            with ScrapeGoat(BASE_URL) as sg:
                jobs = sg.list_jobs()
                assert len(jobs) == 2

    def test_cancel_job(self):
        with respx.mock:
            respx.delete(f"{BASE_URL}/api/v1/jobs/job-1").respond(
                json={"id": "job-1", "status": "cancelled", "url": "https://x.com"}
            )
            with ScrapeGoat(BASE_URL) as sg:
                job = sg.cancel_job("job-1")
                assert job.status == JobStatus.CANCELLED


class TestScrapeGoatErrors:
    """Tests for error handling."""

    def test_auth_error(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/health").respond(401, text="Unauthorized")
            with ScrapeGoat(BASE_URL) as sg:
                with pytest.raises(AuthenticationError):
                    sg.health()

    def test_not_found_error(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/jobs/missing").respond(404, text="not found")
            with ScrapeGoat(BASE_URL) as sg:
                with pytest.raises(NotFoundError):
                    sg.get_job("missing")

    def test_rate_limit_error(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/health").respond(
                429, text="Too many requests", headers={"Retry-After": "10"}
            )
            with ScrapeGoat(BASE_URL) as sg:
                with pytest.raises(RateLimitError) as exc_info:
                    sg.health()
                assert exc_info.value.retry_after == 10.0

    def test_server_error(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/health").respond(500, text="Internal Server Error")
            with ScrapeGoat(BASE_URL) as sg:
                with pytest.raises(ScrapeGoatError) as exc_info:
                    sg.health()
                assert exc_info.value.status_code == 500

    def test_api_key_in_header(self):
        with respx.mock:
            route = respx.get(f"{BASE_URL}/health").respond(json={"status": "ok"})
            with ScrapeGoat(BASE_URL, api_key="sk-test-123") as sg:
                sg.health()
                assert route.calls[0].request.headers["Authorization"] == "Bearer sk-test-123"


# --- Async Client Tests ---


@pytest.mark.asyncio
class TestAsyncScrapeGoat:
    """Tests for the async ScrapeGoat client."""

    async def test_health(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/health").respond(json={"status": "ok"})
            async with AsyncScrapeGoat(BASE_URL) as sg:
                result = await sg.health()
                assert result["status"] == "ok"

    async def test_crawl(self):
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/crawl").respond(
                json={"job_id": "async-1", "status": "pending"}
            )
            async with AsyncScrapeGoat(BASE_URL) as sg:
                result = await sg.crawl("https://example.com")
                assert result.job_id == "async-1"
                assert result.status == JobStatus.PENDING

    async def test_extract(self):
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/extract").respond(
                json={"data": {"title": "Async Product"}, "model": "gpt-4o"}
            )
            async with AsyncScrapeGoat(BASE_URL) as sg:
                result = await sg.extract("https://example.com", {"title": "string"})
                assert result.data["title"] == "Async Product"

    async def test_get_job(self):
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/jobs/j1").respond(
                json={"id": "j1", "status": "running", "url": "https://x.com"}
            )
            async with AsyncScrapeGoat(BASE_URL) as sg:
                job = await sg.get_job("j1")
                assert job.status == JobStatus.RUNNING
