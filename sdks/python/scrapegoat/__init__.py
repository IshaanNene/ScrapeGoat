"""
ScrapeGoat Python SDK — Typed client for the ScrapeGoat REST API.

Usage:
    from scrapegoat import ScrapeGoat

    sg = ScrapeGoat("http://localhost:8080", api_key="sk-...")
    result = sg.crawl("https://example.com", depth=2)
    print(result.items)
"""

from scrapegoat.client import ScrapeGoat, AsyncScrapeGoat
from scrapegoat.models import (
    CrawlRequest,
    CrawlResult,
    ExtractRequest,
    ExtractResult,
    Job,
    JobStatus,
    SEOAuditResult,
)

__version__ = "0.1.0"
__all__ = [
    "ScrapeGoat",
    "AsyncScrapeGoat",
    "CrawlRequest",
    "CrawlResult",
    "ExtractRequest",
    "ExtractResult",
    "Job",
    "JobStatus",
    "SEOAuditResult",
]
