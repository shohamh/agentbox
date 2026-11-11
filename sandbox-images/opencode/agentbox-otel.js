// Emits one OTLP log per tool call, before and after it runs, to the
// appliance's collector (through the manager's OTLP proxy, which stamps the
// sandbox ID). The correlator matches these against gVisor process events.
const endpoint = (process.env.OTEL_EXPORTER_OTLP_ENDPOINT || "http://otel.agentbox:4318") + "/v1/logs";
const sandbox = process.env.AGENTBOX_SANDBOX || "";

function emit(phase, tool, args, extra) {
  const attr = (key, value) => ({ key, value: { stringValue: String(value ?? "") } });
  const now = String(Date.now() * 1e6);
  const body = {
    resourceLogs: [{
      resource: { attributes: [attr("service.name", "opencode")] },
      scopeLogs: [{
        scope: { name: "agentbox-opencode-plugin" },
        logRecords: [{
          timeUnixNano: now, observedTimeUnixNano: now, severityText: "INFO", severityNumber: 9,
          body: { stringValue: "tool.call" },
          attributes: [
            attr("event.name", "tool.call"), attr("agentbox.sandbox", sandbox), attr("harness", "opencode"),
            attr("phase", phase), attr("tool", tool), attr("args", JSON.stringify(args ?? {})), ...extra,
          ],
        }],
      }],
    }],
  };
  return fetch(endpoint, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) }).catch(() => {});
}

export const AgentboxOtel = async () => ({
  "tool.execute.before": async (input, output) => {
    await emit("before", input.tool, output.args, []);
  },
  "tool.execute.after": async (input, output) => {
    await emit("after", input.tool, input.args ?? {}, [{ key: "title", value: { stringValue: String(output?.title ?? "") } }]);
  },
});
