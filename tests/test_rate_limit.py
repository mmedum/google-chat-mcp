"""Token-bucket rate limiter behaviour."""

from __future__ import annotations

import pytest
from src.rate_limit import ActiveUserTracker, TokenBucketLimiter


@pytest.mark.asyncio
async def test_rejects_once_bucket_drained() -> None:
    lim = TokenBucketLimiter(capacity=3)
    t = 0.0
    assert await lim.allow("u", now=t)
    assert await lim.allow("u", now=t)
    assert await lim.allow("u", now=t)
    assert not await lim.allow("u", now=t)


@pytest.mark.asyncio
async def test_refills_over_time() -> None:
    lim = TokenBucketLimiter(capacity=60, window_seconds=60.0)
    # Drain.
    for _ in range(60):
        assert await lim.allow("u", now=0.0)
    assert not await lim.allow("u", now=0.0)
    # One second -> one token back.
    assert await lim.allow("u", now=1.0)


@pytest.mark.asyncio
async def test_separate_buckets_per_user() -> None:
    lim = TokenBucketLimiter(capacity=1)
    assert await lim.allow("a", now=0.0)
    assert not await lim.allow("a", now=0.0)
    assert await lim.allow("b", now=0.0)


@pytest.mark.asyncio
async def test_active_user_tracker_ages_out() -> None:
    tr = ActiveUserTracker(window_seconds=10.0)
    await tr.touch("a")
    count = await tr.touch("b")
    assert count == 2
    # Directly mutate: make 'a' older than the window.
    tr._seen["a"] = -1000.0
    count = await tr.touch("c")
    assert count == 2  # b, c — a expired.
