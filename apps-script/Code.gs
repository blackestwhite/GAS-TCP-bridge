const BROKER_BASE = "https://your-domain.com";
const BRIDGE_TOKEN = "change-me";
const SCRIPT_PROP_BROKER_BASE = "BROKER_BASE";
const SCRIPT_PROP_BRIDGE_TOKEN = "BRIDGE_TOKEN";

function doGet(e) {
  return handleRelayRequest_("get", e);
}

function doPost(e) {
  return handleRelayRequest_("post", e);
}

function handleRelayRequest_(method, e) {
  const sid = bridgeSid_(e);
  const ack = bridgeAck_(e);
  try {
    const cfg = loadRelayConfig_();
    const op = param_(e, "op");

    if (method === "get") {
      if (op !== "down") {
        return downErrorResponse_(sid, ack, "unsupported op");
      }
      return relayDown_(cfg, sid, ack);
    }

    if (op !== "up") {
      return ackErrorResponse_(sid, "unsupported op");
    }
    return relayUp_(cfg, sid, e);
  } catch (err) {
    const message = errorMessage_(err);
    if (method === "get") {
      return downErrorResponse_(sid, ack, message);
    }
    return ackErrorResponse_(sid, message);
  }
}

function loadRelayConfig_() {
  const props = PropertiesService.getScriptProperties();
  const brokerBase = normalizeBaseUrl_(firstNonEmpty_(
    props.getProperty(SCRIPT_PROP_BROKER_BASE),
    BROKER_BASE
  ));
  const bridgeToken = firstNonEmpty_(
    props.getProperty(SCRIPT_PROP_BRIDGE_TOKEN),
    BRIDGE_TOKEN
  );

  if (!brokerBase || brokerBase === "https://your-domain.com") {
    throw new Error("BROKER_BASE is not configured");
  }
  if (!bridgeToken || bridgeToken === "change-me") {
    throw new Error("BRIDGE_TOKEN is not configured");
  }
  return {
    brokerBase: brokerBase,
    bridgeToken: bridgeToken
  };
}

function relayDown_(cfg, sid, ack) {
  const url = cfg.brokerBase + "/down?sid=" + encodeURIComponent(sid) + "&ack=" + encodeURIComponent(String(ack));
  const resp = fetchBroker_(url, cfg.bridgeToken);
  if (resp.error) {
    return downErrorResponse_(sid, ack, resp.error);
  }
  return jsonText_(resp.body);
}

function relayUp_(cfg, sid, e) {
  const url = cfg.brokerBase + "/up?sid=" + encodeURIComponent(sid);
  const body = e && e.postData && e.postData.contents ? e.postData.contents : "{}";
  const resp = fetchBroker_(url, cfg.bridgeToken, body);
  if (resp.error) {
    return ackErrorResponse_(sid, resp.error);
  }
  return jsonText_(resp.body);
}

function fetchBroker_(url, bridgeToken, body) {
  const options = {
    method: body === undefined ? "get" : "post",
    muteHttpExceptions: true,
    headers: {
      "X-Bridge-Token": bridgeToken,
      "Cache-Control": "no-store"
    }
  };
  if (body !== undefined) {
    options.contentType = "application/json";
    options.payload = body;
  }

  let resp;
  try {
    resp = UrlFetchApp.fetch(url, options);
  } catch (err) {
    return {error: "broker fetch failed: " + errorMessage_(err)};
  }

  const code = resp.getResponseCode();
  const headers = resp.getHeaders();
  const text = resp.getContentText();
  if (code >= 200 && code < 300 && looksJson_(headers, text)) {
    return {body: text};
  }
  return {error: brokerResponseError_(code, headers, text)};
}

function brokerResponseError_(code, headers, text) {
  const contentType = headerValue_(headers, "Content-Type");
  const prefix = bodyPrefix_(text);
  if (code >= 200 && code < 300) {
    return "broker returned non-json status=" + code +
      " content_type=" + JSON.stringify(contentType) +
      " body_prefix=" + JSON.stringify(prefix);
  }
  return "broker status=" + code +
    " content_type=" + JSON.stringify(contentType) +
    " body_prefix=" + JSON.stringify(prefix);
}

function downErrorResponse_(sid, ack, message) {
  return jsonText_({
    sid: sid || "",
    ack: ack || 0,
    chunks: [{
      sid: sid || "",
      seq: (ack || 0) + 1,
      type: "error",
      message: message
    }]
  });
}

function ackErrorResponse_(sid, message) {
  return jsonText_({
    sid: sid || "",
    ack: 0,
    error: message
  });
}

function param_(e, name) {
  return e && e.parameter && e.parameter[name] ? e.parameter[name] : "";
}

function bridgeSid_(e) {
  return param_(e, "bsid") || param_(e, "sid");
}

function bridgeAck_(e) {
  const raw = param_(e, "ack") || "0";
  const ack = Number(raw);
  if (!Number.isFinite(ack) || ack < 0) {
    return 0;
  }
  return Math.floor(ack);
}

function jsonText_(value) {
  const text = typeof value === "string" ? value : JSON.stringify(value);
  return ContentService
    .createTextOutput(text)
    .setMimeType(ContentService.MimeType.JSON);
}

function errorMessage_(err) {
  if (err && err.message) {
    return String(err.message);
  }
  return String(err);
}

function firstNonEmpty_(a, b) {
  if (a && String(a).trim() !== "") {
    return String(a).trim();
  }
  if (b && String(b).trim() !== "") {
    return String(b).trim();
  }
  return "";
}

function normalizeBaseUrl_(value) {
  return String(value || "").replace(/\/+$/, "");
}

function headerValue_(headers, name) {
  if (!headers) {
    return "";
  }
  if (headers[name]) {
    return String(headers[name]);
  }
  const lower = name.toLowerCase();
  if (headers[lower]) {
    return String(headers[lower]);
  }
  return "";
}

function looksJson_(headers, text) {
  const contentType = headerValue_(headers, "Content-Type").toLowerCase();
  if (contentType.indexOf("json") !== -1) {
    return true;
  }
  const trimmed = String(text || "").trim();
  return trimmed.indexOf("{") === 0 || trimmed.indexOf("[") === 0;
}

function bodyPrefix_(text) {
  const value = String(text || "").trim();
  if (value.length <= 160) {
    return value;
  }
  return value.slice(0, 160);
}
