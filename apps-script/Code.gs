const BROKER_BASE = "https://your-domain.com";
const BRIDGE_TOKEN = "change-me";

function doGet(e) {
  const op = param(e, "op");
  const sid = bridgeSid(e);
  if (op !== "down") {
    return jsonText('{"error":"unsupported op"}');
  }

  const ack = param(e, "ack") || "0";
  const url = BROKER_BASE + "/down?sid=" + encodeURIComponent(sid) + "&ack=" + encodeURIComponent(ack);
  const resp = UrlFetchApp.fetch(url, {
    method: "get",
    muteHttpExceptions: true,
    headers: {
      "X-Bridge-Token": BRIDGE_TOKEN,
      "Cache-Control": "no-store"
    }
  });
  return jsonText(resp.getContentText());
}

function doPost(e) {
  const op = param(e, "op");
  const sid = bridgeSid(e);
  if (op !== "up") {
    return jsonText('{"error":"unsupported op"}');
  }

  const url = BROKER_BASE + "/up?sid=" + encodeURIComponent(sid);
  const body = e && e.postData && e.postData.contents ? e.postData.contents : "{}";
  const resp = UrlFetchApp.fetch(url, {
    method: "post",
    contentType: "application/json",
    payload: body,
    muteHttpExceptions: true,
    headers: {
      "X-Bridge-Token": BRIDGE_TOKEN,
      "Cache-Control": "no-store"
    }
  });
  return jsonText(resp.getContentText());
}

function param(e, name) {
  return e && e.parameter && e.parameter[name] ? e.parameter[name] : "";
}

function bridgeSid(e) {
  return param(e, "bsid") || param(e, "sid");
}

function jsonText(text) {
  return ContentService
    .createTextOutput(text)
    .setMimeType(ContentService.MimeType.JSON);
}
