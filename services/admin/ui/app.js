"use strict";

// Podsync admin UI. Built with DOM APIs and textContent only, so values from feeds and errors
// can never inject markup.

const REFRESH_MS = 30000;

function el(tag, attrs, children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "class") node.className = value;
    else if (key === "text") node.textContent = value;
    else node.setAttribute(key, value);
  }
  for (const child of [].concat(children || [])) {
    if (child == null) continue;
    node.append(typeof child === "string" ? document.createTextNode(child) : child);
  }
  return node;
}

async function api(path) {
  const response = await fetch(path, { headers: { Accept: "application/json" }, credentials: "same-origin" });
  if (!response.ok) {
    const text = (await response.text()).trim();
    throw new Error(`${response.status} ${response.statusText}${text ? ": " + text : ""}`);
  }
  return response.json();
}

// apiSend makes a state-changing request. It always resolves with { ok, status, data } so callers
// can handle validation errors (422) and conflicts (409) themselves.
async function apiSend(method, path, body) {
  const response = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", Accept: "application/json", "X-Podsync-Admin": "1" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data = null;
  const text = await response.text();
  try {
    data = text ? JSON.parse(text) : null;
  } catch (err) {
    data = { error: text.trim() || response.statusText };
  }
  return { ok: response.ok, status: response.status, data };
}

function formatBytes(size) {
  if (size < 1024) return `${size} B`;
  return `${(size / 1024).toFixed(1)} KB`;
}

function relative(iso) {
  if (!iso) return "";
  const diff = (new Date(iso).getTime() - Date.now()) / 1000;
  const abs = Math.abs(diff);
  const units = [["day", 86400], ["hour", 3600], ["minute", 60]];
  for (const [unit, seconds] of units) {
    if (abs >= seconds) {
      const value = Math.round(abs / seconds);
      const label = `${value} ${unit}${value === 1 ? "" : "s"}`;
      return diff < 0 ? `${label} ago` : `in ${label}`;
    }
  }
  return diff < 0 ? "just now" : "in under a minute";
}

function exact(iso) {
  return iso ? new Date(iso).toLocaleString() : "";
}

// ----- Feeds view -----

const EPISODE_ORDER = [
  ["published", "ok"], ["downloaded", "ok"], ["stored", "ok"],
  ["new", ""], ["planned", ""], ["downloading", "warn"], ["processing", "warn"],
  ["error", "bad"], ["cleaned", ""],
];

function lastRun(feed) {
  const success = feed.last_success_at ? new Date(feed.last_success_at).getTime() : 0;
  const failure = feed.last_failure_at ? new Date(feed.last_failure_at).getTime() : 0;
  if (!success && !failure) {
    return el("div", {}, [el("span", { class: "state idle", text: feed.synced ? "Not run yet" : "Waiting for first sync" })]);
  }
  if (failure > success) {
    return el("div", {}, [
      el("span", { class: "state bad", text: "Failed", title: exact(feed.last_failure_at) }),
      " ", el("span", { class: "muted", text: relative(feed.last_failure_at) }),
      feed.last_failure ? el("div", { class: "error-text", text: feed.last_failure }) : null,
    ]);
  }
  const node = el("div", {}, [
    el("span", { class: "state ok", text: "OK", title: exact(feed.last_success_at) }),
    " ", el("span", { class: "muted", text: relative(feed.last_success_at) }),
  ]);
  if (feed.last_failure) {
    node.append(el("div", { class: "error-text", text: "Episode failures: " + feed.last_failure }));
  }
  return node;
}

function episodeChips(counts) {
  const chips = el("div", { class: "chips" });
  const known = new Set(EPISODE_ORDER.map(([name]) => name));
  for (const [name, tone] of EPISODE_ORDER) {
    if (counts[name]) chips.append(el("span", { class: `chip ${tone}`, text: `${counts[name]} ${name}` }));
  }
  for (const [name, count] of Object.entries(counts)) {
    if (!known.has(name) && count) chips.append(el("span", { class: "chip", text: `${count} ${name}` }));
  }
  if (!chips.childElementCount) chips.append(el("span", { class: "muted", text: "None yet" }));
  return chips;
}

function feedRow(feed) {
  const abs = feed.audiobookshelf
    ? el("div", {}, [el("div", { text: feed.audiobookshelf.directory }), el("div", { class: "muted small", text: `${feed.audiobookshelf.linked} linked` })])
    : el("span", { class: "muted", text: "Off" });

  return el("tr", {}, [
    el("td", {}, [
      el("div", { class: "feed-title", text: feed.title || feed.id }),
      feed.title ? el("div", { class: "feed-id", text: feed.id }) : null,
      el("div", { class: "feed-url", text: feed.url }),
    ]),
    el("td", {}, [
      el("div", {}, [el("code", { text: feed.schedule })]),
      feed.next_run ? el("div", { class: "muted small", text: "Next " + relative(feed.next_run), title: exact(feed.next_run) }) : null,
    ]),
    el("td", {}, [lastRun(feed)]),
    el("td", {}, [episodeChips(feed.episodes || {})]),
    el("td", {}, [abs]),
  ]);
}

function stat(value, label) {
  return el("div", { class: "stat" }, [el("div", { class: "value", text: String(value) }), el("div", { class: "label", text: label })]);
}

function renderStatus(status) {
  const feeds = status.feeds || [];
  const failing = feeds.filter((f) => f.last_failure_at && (!f.last_success_at || new Date(f.last_failure_at) > new Date(f.last_success_at))).length;
  const published = feeds.reduce((sum, f) => sum + ((f.episodes || {}).published || 0), 0);
  const errored = feeds.reduce((sum, f) => sum + ((f.episodes || {}).error || 0), 0);

  document.getElementById("stats").replaceChildren(
    stat(feeds.length, "Feeds"),
    stat(failing, "Failing feeds"),
    stat(published, "Published episodes"),
    stat(errored, "Episodes with errors"),
  );

  const table = document.getElementById("feeds");
  table.tBodies[0].replaceChildren(...feeds.map(feedRow));
  table.hidden = feeds.length === 0;
  document.getElementById("feeds-empty").hidden = feeds.length !== 0;
  if (status.config_path) document.getElementById("config-path").textContent = status.config_path;
  document.getElementById("updated").textContent = "Updated " + new Date(status.generated_at).toLocaleTimeString();
}

async function loadStatus() {
  const errorBox = document.getElementById("feeds-error");
  try {
    renderStatus(await api("api/status"));
    errorBox.hidden = true;
  } catch (err) {
    errorBox.textContent = "Could not load status: " + err.message;
    errorBox.hidden = false;
  }
}

// ----- Settings reference view -----

function typeLabel(schema) {
  if (Array.isArray(schema.type)) return schema.type.join(" or ");
  if (schema.format === "duration") return "duration";
  if (schema.type === "array" && schema.items) return `list of ${schema.items.type === "object" ? "tables" : typeLabel(schema.items)}`;
  if (schema.type === "object" && schema.additionalProperties) return "map";
  return schema.type || "value";
}

function optionNode(name, schema) {
  const head = el("div", { class: "option-head" }, [
    el("span", { class: "option-name", text: name }),
    el("span", { class: "badge", text: typeLabel(schema) }),
    schema["x-secret"] ? el("span", { class: "badge secret", text: "secret" }) : null,
  ]);
  const node = el("div", { class: "option" }, [head]);
  if (schema.description) node.append(el("div", { class: "option-desc", text: schema.description }));
  if (schema.enum) node.append(el("div", { class: "option-desc", text: "One of: " + schema.enum.join(", ") }));

  const nested = schema.properties ? schema
    : schema.items && schema.items.properties ? schema.items
      : schema.additionalProperties && schema.additionalProperties.properties ? schema.additionalProperties
        : null;
  if (nested) {
    const children = el("div", { class: "nested" });
    for (const key of nested["x-order"] || Object.keys(nested.properties)) {
      children.append(optionNode(key, nested.properties[key]));
    }
    node.append(children);
  }
  return node;
}

function renderSchema(schema) {
  const container = document.getElementById("settings");
  const sections = [];
  for (const key of schema["x-order"] || Object.keys(schema.properties || {})) {
    const section = schema.properties[key];
    const body = el("div", { class: "body" });
    const target = section.properties ? section
      : section.additionalProperties && section.additionalProperties.properties ? section.additionalProperties
        : null;
    if (section.description) body.append(el("p", { class: "muted", text: section.description }));
    if (target) {
      if (target !== section) body.append(el("p", { class: "muted small", text: "Each entry, keyed by ID, has these options:" }));
      for (const name of target["x-order"] || Object.keys(target.properties)) body.append(optionNode(name, target.properties[name]));
    } else {
      body.append(optionNode(key, section));
    }
    sections.push(el("details", { class: "section" }, [el("summary", { text: key }), body]));
  }
  container.replaceChildren(...sections);
}

let schemaLoaded = false;
async function loadSchema() {
  if (schemaLoaded) return;
  try {
    renderSchema(await api("api/schema"));
    schemaLoaded = true;
  } catch (err) {
    document.getElementById("settings").replaceChildren(el("div", { class: "notice error", text: "Could not load settings: " + err.message }));
  }
}

// ----- Shell -----

function showView(name) {
  for (const tab of document.querySelectorAll(".tab")) tab.setAttribute("aria-selected", String(tab.dataset.view === name));
  for (const view of document.querySelectorAll(".view")) view.hidden = view.id !== `view-${name}`;
  if (name === "settings") loadSchema();
  if (name === "config" && window.PodsyncEditor) window.PodsyncEditor.show();
  if (name === "history" && window.PodsyncEditor) window.PodsyncEditor.showHistory();
}

async function loadIdentity() {
  try {
    const me = await api("api/me");
    document.getElementById("who").textContent = `${me.user} · ${me.auth_mode} auth · ${me.version}`;
    // With the editor available, it replaces the read-only settings reference.
    for (const tab of document.querySelectorAll("[data-requires-editor]")) tab.hidden = !me.editable;
    for (const tab of document.querySelectorAll("[data-read-only]")) tab.hidden = me.editable;
  } catch (err) {
    document.getElementById("who").textContent = "";
  }
}

document.addEventListener("DOMContentLoaded", () => {
  for (const tab of document.querySelectorAll(".tab")) tab.addEventListener("click", () => showView(tab.dataset.view));
  document.getElementById("refresh").addEventListener("click", loadStatus);
  loadIdentity();
  loadStatus();
  setInterval(() => { if (!document.hidden) loadStatus(); }, REFRESH_MS);
});
