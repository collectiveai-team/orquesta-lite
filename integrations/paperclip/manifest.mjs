export default {
  id: "orquesta.external-flow-relay",
  apiVersion: 1,
  version: "0.1.0",
  displayName: "Orquesta external flow relay",
  description: "Notify Orquesta about assigned work; flow decisions remain in orq-lite.",
  author: "Collective AI",
  categories: ["connector", "automation"],
  capabilities: [
    "events.subscribe", "issues.read", "http.outbound", "secrets.read-ref",
    "plugin.state.read", "plugin.state.write", "instance.settings.register",
  ],
  entrypoints: {worker: "./worker.mjs"},
  instanceConfigSchema: {
    type: "object",
    additionalProperties: false,
    required: ["orquestaUrl", "orquestaToken"],
    properties: {
      orquestaUrl: {type: "string", title: "Orquesta API origin"},
      orquestaToken: {
        type: "object", title: "Orquesta callback token reference",
        required: ["type", "secretId"],
        properties: {type: {const: "secret_ref"}, secretId: {type: "string"}},
      },
    },
  },
};
