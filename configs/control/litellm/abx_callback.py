"""LiteLLM callback: one OTLP log per completed model call (model.call) and one
per tool call the model made (gen_ai.tool_call). The correlator matches the
tool calls against what actually ran in the sandbox.

Identity comes from the x-agentbox-* headers that gatekeeper (user, device,
tier) and the appliance's local Envoy (sandbox) set before the request reaches
LiteLLM.
"""
import json
import os
import time

import httpx
from litellm.integrations.custom_logger import CustomLogger

ENDPOINT = os.environ.get("OTLP_ENDPOINT", "http://otel-gateway:4319/v1/logs")


def _val(v):
    if isinstance(v, bool):
        return {"boolValue": v}
    if isinstance(v, int):
        return {"intValue": str(v)}
    if isinstance(v, float):
        return {"doubleValue": v}
    return {"stringValue": "" if v is None else str(v)}


def _get(obj, name, default=None):
    if obj is None:
        return default
    if isinstance(obj, dict):
        return obj.get(name, default)
    return getattr(obj, name, default)


class AbxLogger(CustomLogger):
    def __init__(self):
        super().__init__()
        self.client = httpx.AsyncClient(timeout=5.0)

    async def _emit(self, records):
        if not records:
            return
        now = str(time.time_ns())
        logs = []
        for name, attrs, sev in records:
            a = [{"key": "event.name", "value": _val(name)}]
            a += [{"key": k, "value": _val(v)} for k, v in attrs.items() if v not in (None, "")]
            logs.append({
                "timeUnixNano": now, "observedTimeUnixNano": now, "severityText": sev,
                "severityNumber": 17 if sev == "ERROR" else 9,
                "body": {"stringValue": name}, "attributes": a,
            })
        body = {"resourceLogs": [{
            "resource": {"attributes": [{"key": "service.name", "value": _val("litellm")}]},
            "scopeLogs": [{"scope": {"name": "agentbox.litellm"}, "logRecords": logs}],
        }]}
        try:
            await self.client.post(ENDPOINT, json=body)
        except Exception as e:  # telemetry must never break a model call
            print(f"abx_callback: send failed: {e}")

    def _common(self, kwargs, response_obj):
        slo = kwargs.get("standard_logging_object") or {}
        lp = kwargs.get("litellm_params") or {}
        headers = ((lp.get("proxy_server_request") or {}).get("headers")) or {}
        headers = {k.lower(): v for k, v in headers.items()}
        meta = lp.get("metadata") or {}
        return {
            "request_id": headers.get("x-request-id") or kwargs.get("litellm_call_id"),
            "gen_ai.request.model": slo.get("model_group") or kwargs.get("model"),
            "gen_ai.response.model": _get(response_obj, "model"),
            "agentbox.user": headers.get("x-agentbox-user") or meta.get("user_api_key_user_id"),
            "agentbox.device": headers.get("x-agentbox-device"),
            "agentbox.sandbox": headers.get("x-agentbox-sandbox"),
            "agentbox.tier": headers.get("x-agentbox-tier"),
            "agentbox.image": headers.get("x-agentbox-image"),
            "agentbox.bundle_version": headers.get("x-agentbox-bundle"),
        }, slo

    def _tool_calls(self, response_obj):
        calls = []
        for ch in _get(response_obj, "choices", None) or []:
            msg = _get(ch, "message")
            for tc in _get(msg, "tool_calls", None) or []:
                fn = _get(tc, "function")
                calls.append((_get(fn, "name"), _get(fn, "arguments")))
        for item in _get(response_obj, "output", None) or []:
            t = _get(item, "type")
            if t == "function_call":
                calls.append((_get(item, "name"), _get(item, "arguments")))
            elif t == "local_shell_call":
                calls.append(("local_shell", json.dumps(_get(_get(item, "action"), "command", None))))
        return calls

    async def async_log_success_event(self, kwargs, response_obj, start_time, end_time):
        common, slo = self._common(kwargs, response_obj)
        usage = _get(response_obj, "usage")
        pt = _get(usage, "prompt_tokens", None) or _get(usage, "input_tokens", None) or slo.get("prompt_tokens")
        ct = _get(usage, "completion_tokens", None) or _get(usage, "output_tokens", None) or slo.get("completion_tokens")
        finish = None
        for ch in _get(response_obj, "choices", None) or []:
            finish = _get(ch, "finish_reason")
        calls = self._tool_calls(response_obj)
        if finish is None and calls:
            finish = "tool_calls"
        attrs = dict(common)
        attrs.update({
            "gen_ai.usage.input_tokens": int(pt or 0), "gen_ai.usage.output_tokens": int(ct or 0),
            "gen_ai.response.finish_reasons": finish or "stop",
            "cost_usd": float(slo.get("response_cost") or 0),
            "latency_ms": int((end_time - start_time).total_seconds() * 1000),
            "tool_calls": len(calls),
        })
        recs = [("model.call", attrs, "INFO")]
        for name, args in calls:
            t = dict(common)
            t.update({"gen_ai.tool.name": name, "gen_ai.tool.arguments": args if isinstance(args, str) else json.dumps(args)})
            recs.append(("gen_ai.tool_call", t, "INFO"))
        await self._emit(recs)

    async def async_log_failure_event(self, kwargs, response_obj, start_time, end_time):
        common, _ = self._common(kwargs, response_obj)
        common["error"] = str(kwargs.get("exception"))[:500]
        await self._emit([("model.error", common, "ERROR")])


handler = AbxLogger()
