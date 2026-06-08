"""Pydantic models for the ScrapeGoat API."""

from __future__ import annotations

from datetime import datetime
from enum import Enum
from typing import Any, Optional

from pydantic import BaseModel, Field


class JobStatus(str, Enum):
    """Job execution status."""

    PENDING = "pending"
    RUNNING = "running"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELLED = "cancelled"


class CrawlRequest(BaseModel):
    """Parameters for a crawl job."""

    url: str = Field(..., description="Seed URL to crawl")
    depth: int = Field(3, ge=0, le=100, description="Maximum crawl depth")
    concurrency: int = Field(10, ge=1, le=100, description="Concurrent workers")
    allowed_domains: list[str] = Field(default_factory=list, description="Restrict crawl to these domains")
    selectors: dict[str, str] = Field(default_factory=dict, description="CSS selectors for extraction")
    max_requests: int = Field(0, ge=0, description="Max requests (0 = unlimited)")
    respect_robots: bool = Field(True, description="Respect robots.txt")
    user_agent: Optional[str] = Field(None, description="Custom User-Agent string")


class ExtractRequest(BaseModel):
    """Parameters for LLM-based extraction."""

    url: str = Field(..., description="URL to extract from")
    schema: dict[str, Any] = Field(..., description="JSON schema defining fields to extract")
    model: Optional[str] = Field(None, description="LLM model override (e.g., gpt-4o)")


class CrawlResult(BaseModel):
    """Result of a crawl operation."""

    job_id: str
    status: JobStatus
    items: list[dict[str, Any]] = Field(default_factory=list)
    items_count: int = 0
    pages_crawled: int = 0
    duration_ms: int = 0
    errors: list[str] = Field(default_factory=list)


class ExtractResult(BaseModel):
    """Result of an LLM extraction."""

    url: str
    data: dict[str, Any] = Field(default_factory=dict)
    model: str = ""
    tokens_used: int = 0
    cached: bool = False
    cost_usd: float = 0.0


class SEOAuditResult(BaseModel):
    """Result of an SEO audit."""

    url: str
    score: int = 0
    issues: list[dict[str, Any]] = Field(default_factory=list)
    meta: dict[str, Any] = Field(default_factory=dict)


class Job(BaseModel):
    """A crawl or extraction job."""

    id: str
    status: JobStatus
    url: str = ""
    created_at: Optional[datetime] = None
    started_at: Optional[datetime] = None
    completed_at: Optional[datetime] = None
    error: Optional[str] = None
    items_count: int = 0
    pages_crawled: int = 0
    result: Optional[CrawlResult] = None
