from __future__ import annotations

from typing import Any

from vlogs.client import VictoriaLogsClient, _field_expr, _quote_logsql_value


class StubVictoriaLogsClient(VictoriaLogsClient):
    def __init__(self, entries: list[dict[str, Any]]) -> None:
        super().__init__(url="http://unused")
        self.entries = entries

    def query(
        self,
        query: str,
        limit: int = 100,
        start: str | None = None,
        end: str | None = None,
    ) -> list[dict]:
        return self.entries


def test_quote_logsql_value_handles_slack_thread_key() -> None:
    assert (
        _field_expr("thread_key", "C0AJ07U8Z1N:1777910337.403889")
        == 'thread_key:"C0AJ07U8Z1N:1777910337.403889"'
    )


def test_quote_logsql_value_escapes_quotes_and_backslashes() -> None:
    assert _quote_logsql_value('a"b\\c') == '"a\\"b\\\\c"'


def test_tool_calls_exposes_tool_args() -> None:
    client = StubVictoriaLogsClient(
        [
            {
                "_time": "2026-06-29T12:00:00Z",
                "_stream": "ignored",
                "event": "tool_call_completed",
                "tool_name": "websearch",
                "tool_method": "cli",
                "tool_args_count": 2,
                "duration_ms": "42",
                "success": "true",
            }
        ]
    )

    assert client.tool_calls() == [
        {
            "_time": "2026-06-29T12:00:00Z",
            "duration_ms": "42",
            "success": "true",
            "tool_args_count": 2,
            "tool_name": "websearch",
            "tool_method": "cli",
        }
    ]


def test_tool_analytics_counts_cli_arg_patterns() -> None:
    client = StubVictoriaLogsClient(
        [
            {
                "tool_name": "websearch",
                "tool_method": "cli",
                "duration_ms": "10",
                "success": "true",
            },
            {
                "tool_name": "websearch",
                "tool_method": "cli",
                "duration_ms": "20",
                "success": "true",
            },
            {
                "tool_name": "websearch",
                "tool_method": "cli",
                "duration_ms": "30",
                "success": "false",
            },
            {
                "tool_name": "slack",
                "tool_method": "cli",
                "duration_ms": "5",
                "success": "true",
            },
        ]
    )

    assert client.tool_analytics() == [
        {
            "tool": "websearch",
            "calls": 3,
            "failures": 1,
            "failure_rate_pct": 33.3,
            "avg_duration_ms": 20,
            "methods": {"cli": 3},
        },
        {
            "tool": "slack",
            "calls": 1,
            "failures": 0,
            "failure_rate_pct": 0.0,
            "avg_duration_ms": 5,
            "methods": {"cli": 1},
        },
    ]
