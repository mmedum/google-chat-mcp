"""Tests for the test infrastructure itself.

`_httpx2_mock` is what every HTTP test in this suite relies on. If it stops
intercepting, tests do not fail loudly — they either reach the real network or
quietly assert nothing. That already happened once during the httpx2
migration, when clients built by the code under test escaped interception and
the doctor tests made real calls to Google. These pin the properties that
prevent it.
"""

from __future__ import annotations

import httpx2
import pytest

from ._httpx2_mock import (
    UnmockedRequest,
    dispatch_transport,
    intercept_all_clients,
    mock_api,
)

_URL = "https://api.test/v1/thing"


async def _get(url: str = _URL) -> httpx2.Response:
    async with httpx2.AsyncClient(transport=dispatch_transport()) as c:
        return await c.get(url)


@pytest.mark.asyncio
async def test_request_outside_any_router_raises() -> None:
    """The guard that keeps a stray request off the network."""
    with pytest.raises(UnmockedRequest, match="no active mock_api"):
        await _get()


@pytest.mark.asyncio
async def test_unmocked_request_inside_a_router_raises() -> None:
    """A URL nobody registered must fail, not return a silent default."""
    with (
        mock_api(assert_all_called=False),
        pytest.raises(UnmockedRequest, match="unmocked request"),
    ):
        await _get("https://api.test/v1/unregistered")


@pytest.mark.asyncio
async def test_registered_but_uncalled_route_fails_the_block() -> None:
    with pytest.raises(AssertionError, match="never called"), mock_api() as mock:
        mock.get(_URL).mock(return_value=httpx2.Response(200))


@pytest.mark.asyncio
async def test_assert_all_called_false_tolerates_an_unused_route() -> None:
    with mock_api(assert_all_called=False) as mock:
        route = mock.get(_URL).mock(return_value=httpx2.Response(200))
    assert route.call_count == 0


@pytest.mark.asyncio
async def test_side_effect_runs_in_order_then_holds() -> None:
    """Retry tests depend on a short list running out and repeating its last entry."""
    with mock_api(assert_all_called=False) as mock:
        route = mock.get(_URL).mock(
            side_effect=[httpx2.Response(500), httpx2.Response(503), httpx2.Response(200)]
        )
        codes = [(await _get()).status_code for _ in range(4)]
    assert codes == [500, 503, 200, 200]
    assert route.call_count == 4
    assert route.calls.last.response is not None


@pytest.mark.asyncio
async def test_side_effect_accepts_a_bare_exception() -> None:
    with mock_api(assert_all_called=False) as mock:
        mock.get(_URL).mock(side_effect=httpx2.ConnectError("refused"))
        with pytest.raises(httpx2.ConnectError):
            await _get()


@pytest.mark.asyncio
async def test_base_url_and_lookup_forms_all_match() -> None:
    with mock_api(base_url="https://api.test/v1") as mock:
        exact = mock.get("/thing").mock(return_value=httpx2.Response(200))
        prefixed = mock.get(url__startswith="https://api.test/v1/pre").mock(
            return_value=httpx2.Response(201)
        )
        regexed = mock.get(url__regex=r".*/re/\d+").mock(return_value=httpx2.Response(202))
        assert (await _get()).status_code == 200
        assert (await _get("https://api.test/v1/prefixed/x")).status_code == 201
        assert (await _get("https://api.test/v1/re/42")).status_code == 202
    assert (exact.call_count, prefixed.call_count, regexed.call_count) == (1, 1, 1)


@pytest.mark.asyncio
async def test_query_string_is_ignored_when_matching_but_kept_on_the_request() -> None:
    with mock_api(base_url="https://api.test/v1") as mock:
        route = mock.get("/thing").mock(return_value=httpx2.Response(200))
        await _get(f"{_URL}?pageSize=50&filter=x")
    assert route.calls[0].request.url.params["pageSize"] == "50"


def test_nested_routers_are_refused() -> None:
    """Two active routers would make "which one answers" ambiguous."""
    with (
        mock_api(assert_all_called=False),
        pytest.raises(RuntimeError, match="already active"),
        mock_api(),
    ):
        pass


def test_route_rejects_a_non_httpx2_response() -> None:
    """The mistake that made the whole migration necessary, pinned."""
    with (
        mock_api(assert_all_called=False) as mock,
        pytest.raises(TypeError, match=r"not an httpx2\.Response"),
    ):
        # Deliberately the wrong type — that is the assertion.
        mock.get(_URL).mock(return_value=object())  # ty: ignore[invalid-argument-type]


def test_route_requires_exactly_one_lookup() -> None:
    with mock_api(assert_all_called=False) as mock:
        with pytest.raises(TypeError, match="exactly one"):
            mock.get(_URL, url__regex=".*")
        with pytest.raises(TypeError, match="exactly one"):
            mock.get()


@pytest.mark.asyncio
async def test_interception_catches_a_client_the_code_under_test_builds() -> None:
    """The regression that let doctor tests call Google for real.

    Injecting a transport into the fixture's client is not enough: production
    code constructs its own `AsyncClient`, and those must be caught too.
    """
    with mock_api(base_url="https://api.test/v1") as mock, intercept_all_clients():
        mock.get("/thing").mock(return_value=httpx2.Response(200))
        # No transport named — exactly what ChatClient does when built by the
        # HTTPS lifespan or the stdio doctor.
        async with httpx2.AsyncClient() as client:
            assert (await client.get(_URL)).status_code == 200


@pytest.mark.asyncio
async def test_interception_leaves_an_explicit_transport_alone() -> None:
    """The ASGI integration harness depends on naming its own transport."""
    sentinel = httpx2.MockTransport(lambda _req: httpx2.Response(418))
    with mock_api(assert_all_called=False), intercept_all_clients():
        async with httpx2.AsyncClient(transport=sentinel) as client:
            assert (await client.get(_URL)).status_code == 418


@pytest.mark.asyncio
async def test_a_hit_on_an_unmocked_route_is_still_recorded() -> None:
    """The bug that made every `assert not route.called` guard dead.

    Resolving ran before the call was recorded, so a request to a route with
    no `.mock()` raised without registering — and the guard passed for a
    request that had in fact been made.
    """
    with mock_api(assert_all_called=False) as mock:
        route = mock.delete(_URL)
        with pytest.raises(UnmockedRequest):
            async with httpx2.AsyncClient(transport=dispatch_transport()) as c:
                await c.delete(_URL)
    assert route.call_count == 1, "the call must be visible to `assert not route.called`"
    assert route.called is True


@pytest.mark.asyncio
async def test_unmocked_request_survives_a_broad_except() -> None:
    """Handlers under test catch `except Exception` — this must not be swallowed."""

    async def swallow_like_the_code_under_test() -> None:
        try:
            await _get("https://api.test/v1/nope")
        except Exception:
            pytest.fail("UnmockedRequest was swallowed by a broad except")

    with mock_api(assert_all_called=False), pytest.raises(UnmockedRequest):
        await swallow_like_the_code_under_test()


@pytest.mark.asyncio
async def test_regex_and_prefix_routes_are_anchored_to_base_url() -> None:
    """A route must not answer for an unrelated origin just because the path matches."""
    with mock_api(base_url="https://people.test/v1", assert_all_called=False) as mock:
        mock.get(url__regex=r".*/people/.*").mock(return_value=httpx2.Response(200))
        assert (await _get("https://people.test/v1/people/me")).status_code == 200
        with pytest.raises(UnmockedRequest):
            await _get("https://chat.test/v1/people/me")


@pytest.mark.asyncio
async def test_interception_covers_the_sync_client_too() -> None:
    """A sync `httpx2.Client` escaped the first version and reached Google."""
    with mock_api(base_url="https://api.test/v1", assert_all_called=False) as mock:
        mock.get("/thing").mock(return_value=httpx2.Response(200))
        with intercept_all_clients(), httpx2.Client() as client:
            assert client.get(_URL).status_code == 200


@pytest.mark.asyncio
async def test_interception_drops_mounts_that_would_bypass_it() -> None:
    """`mounts=` wins over `transport=` per pattern, so it must not survive."""
    escaped = httpx2.MockTransport(lambda _req: httpx2.Response(599))
    with mock_api(base_url="https://api.test/v1", assert_all_called=False) as mock:
        mock.get("/thing").mock(return_value=httpx2.Response(200))
        with intercept_all_clients():
            async with httpx2.AsyncClient(mounts={"all://": escaped}) as client:
                assert (await client.get(_URL)).status_code == 200, "mount bypassed the router"
