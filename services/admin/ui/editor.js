"use strict";

// Podsync configuration editor. Forms are generated from the configuration schema; values are
// edited on a copy of the document and sent back whole. Secrets arrive as a placeholder and are
// only replaced when the user enters a new value. Built with DOM APIs and textContent only.

(function () {
  const PLACEHOLDER = "__podsync_secret_unchanged__";
  const FEED_ID = /^[A-Za-z0-9_-]+$/;
  const PROVIDERS = ["youtube", "vimeo", "soundcloud", "twitch", "rumble"];

  const state = {
    schema: null,
    snapshot: null,
    original: null,
    doc: null,
    openPaths: new Set(),
    // pending holds new map entries (such as a token provider) that have no value yet, by map path.
    pending: {},
    loading: null,
  };

  // ----- document helpers -----

  const clone = (value) => (value === undefined ? undefined : JSON.parse(JSON.stringify(value)));

  function stableStringify(value) {
    if (Array.isArray(value)) return "[" + value.map(stableStringify).join(",") + "]";
    if (value && typeof value === "object") {
      return "{" + Object.keys(value).sort().map((key) => JSON.stringify(key) + ":" + stableStringify(value[key])).join(",") + "}";
    }
    return JSON.stringify(value);
  }

  // docKey finds the spelling of name used in obj (the decoder matches keys case-insensitively).
  function docKey(obj, name) {
    if (!obj || typeof obj !== "object") return name;
    if (Object.prototype.hasOwnProperty.call(obj, name)) return name;
    const lower = name.toLowerCase();
    return Object.keys(obj).find((key) => key.toLowerCase() === lower) || name;
  }

  function getAt(path) {
    let node = state.doc;
    for (const segment of path) {
      if (node == null || typeof node !== "object") return undefined;
      node = Array.isArray(node) ? node[segment] : node[docKey(node, segment)];
    }
    return node;
  }

  function setAt(path, value) {
    let node = state.doc;
    for (let i = 0; i < path.length - 1; i++) {
      const key = Array.isArray(node) ? path[i] : docKey(node, path[i]);
      if (node[key] == null || typeof node[key] !== "object") node[key] = {};
      node = node[key];
    }
    const last = path[path.length - 1];
    node[Array.isArray(node) ? last : docKey(node, last)] = value;
    changed();
  }

  // deleteAt removes a value and any nested tables it leaves empty (top-level sections stay).
  function deleteAt(path) {
    const parents = [];
    let node = state.doc;
    for (let i = 0; i < path.length - 1; i++) {
      if (node == null || typeof node !== "object") return;
      parents.push([node, Array.isArray(node) ? path[i] : docKey(node, path[i])]);
      node = node[parents[parents.length - 1][1]];
    }
    if (node == null || typeof node !== "object") return;
    const last = path[path.length - 1];
    if (Array.isArray(node)) node.splice(last, 1);
    else delete node[docKey(node, last)];
    for (let i = parents.length - 1; i >= 1; i--) {
      const [parent, key] = parents[i];
      const child = parent[key];
      if (child && typeof child === "object" && !Array.isArray(child) && Object.keys(child).length === 0) delete parent[key];
      else break;
    }
    changed();
  }

  const pathKey = (path) => path.map((p) => String(p).toLowerCase()).join("\u0000");

  function envOverride(path) {
    const key = pathKey(path);
    return (state.snapshot.env_overrides || []).find((o) => pathKey(o.path) === key);
  }

  // ----- schema helpers -----

  function kind(schema) {
    if (!schema) return "string";
    if (Array.isArray(schema.type)) return "flex";
    if (schema.format === "duration") return "duration";
    if (schema.enum) return "enum";
    if (schema.type === "array") return schema.items && schema.items.type === "object" ? "objectlist" : "list";
    if (schema.type === "object") {
      if (schema.properties) return "object";
      const entry = schema.additionalProperties;
      return entry && entry.type === "object" && entry.properties ? "map" : "scalarmap";
    }
    return schema.type || "string";
  }

  const orderOf = (schema) => schema["x-order"] || Object.keys(schema.properties || {});
  const isSecret = (schema) => Boolean(schema && schema["x-secret"]);

  // ----- state -----

  function dirty() {
    return state.doc && stableStringify(state.doc) !== stableStringify(state.original);
  }

  function changed() {
    const isDirty = dirty();
    document.getElementById("editor-state").textContent = isDirty ? "Unsaved changes" : "No changes";
    document.getElementById("editor-state").className = isDirty ? "state warn-text" : "muted";
    document.getElementById("editor-save").disabled = !isDirty;
    document.getElementById("editor-discard").disabled = !isDirty;
  }

  function message(container, tone, title, items, action) {
    const box = el("div", { class: `notice ${tone}` }, [el("div", { class: "notice-title", text: title })]);
    if (items && items.length) box.append(el("ul", {}, items.map((item) => el("li", { text: item }))));
    if (action) box.append(action);
    container.replaceChildren(box);
  }

  function clearMessages() {
    document.getElementById("editor-messages").replaceChildren();
  }

  function showRestart(sections) {
    const box = document.getElementById("editor-restart");
    box.hidden = !sections || sections.length === 0;
    if (!box.hidden) {
      box.textContent = `Restart Podsync to apply saved changes to: ${sections.map((s) => `[${s}]`).join(", ")}. Everything else is already applied.`;
    }
  }

  function showPreview(text) {
    const wrap = document.getElementById("editor-preview-wrap");
    wrap.hidden = !text;
    document.getElementById("editor-preview").textContent = text || "";
  }

  // ----- loading -----

  async function load(force) {
    if (state.loading) return state.loading;
    if (state.doc && !force) return undefined;
    state.loading = (async () => {
      try {
        if (!state.schema) state.schema = await api("api/schema");
        state.snapshot = await api("api/config");
        state.original = clone(state.snapshot.document) || {};
        state.doc = clone(state.original);
        state.pending = {};
        document.getElementById("editor-file").textContent = `${state.snapshot.path} (${state.snapshot.format.toUpperCase()})`;
        showRestart(state.snapshot.pending_restart);
        showPreview("");
        render();
        changed();
      } catch (err) {
        message(document.getElementById("editor-messages"), "error", "Could not load the configuration", [err.message]);
      } finally {
        state.loading = null;
      }
    })();
    return state.loading;
  }

  // ----- rendering -----

  function render() {
    const form = document.getElementById("editor-form");
    const sections = [];
    for (const name of orderOf(state.schema)) {
      const schema = state.schema.properties[name];
      sections.push(section([name], name, schema));
    }
    form.replaceChildren(...sections);
  }

  function collapsible(path, summaryChildren, bodyChildren, className) {
    const details = el("details", { class: className || "section", "data-path": path.join(".") }, [el("summary", {}, summaryChildren), el("div", { class: "body" }, bodyChildren)]);
    const key = pathKey(path);
    details.open = state.openPaths.has(key);
    details.addEventListener("toggle", () => {
      if (details.open) state.openPaths.add(key);
      else state.openPaths.delete(key);
    });
    return details;
  }

  function section(path, name, schema) {
    const body = [];
    if (schema.description) body.push(el("p", { class: "muted", text: schema.description }));
    const k = kind(schema);
    if (k === "object") body.push(...objectFields(path, schema));
    else if (k === "map") body.push(mapEditor(path, schema));
    else if (k === "scalarmap") body.push(scalarMapEditor(path, schema));
    else body.push(field(path, name, schema));
    return collapsible(path, [el("span", { class: "section-name", text: name })], body);
  }

  function objectFields(path, schema) {
    return orderOf(schema).map((name) => field(path.concat(name), name, schema.properties[name]));
  }

  function field(path, name, schema) {
    const k = kind(schema);
    if (k === "object") {
      return collapsible(path, [el("span", { class: "option-name", text: name }), schema.description ? el("span", { class: "muted small", text: " " + schema.description }) : null],
        objectFields(path, schema), "subsection");
    }
    if (k === "objectlist") return objectListEditor(path, name, schema);
    if (k === "map") return collapsible(path, [el("span", { class: "option-name", text: name })], [mapEditor(path, schema)], "subsection");
    if (k === "scalarmap") return collapsible(path, [el("span", { class: "option-name", text: name })], [scalarMapEditor(path, schema)], "subsection");

    return fieldRow(path, name, schema, isSecret(schema) ? secretControl(path, schema) : valueControl(path, schema, k));
  }

  // fieldRow lays out a label, control and help. Options set by an environment variable are shown
  // but disabled: a disabled fieldset also disables controls a secret editor creates later.
  function fieldRow(path, name, schema, control) {
    const override = envOverride(path);
    const label = el("label", { class: "field-label" }, [el("span", { class: "option-name", text: name })]);
    const controlWrap = el("div", { class: "field-control" });
    if (override) {
      label.append(el("span", { class: "badge env", text: "set by " + override.variable }));
      controlWrap.append(el("fieldset", { class: "plain", disabled: "" }, [control]));
      controlWrap.append(el("div", { class: "muted small", text: `The environment variable wins over this file; changes here take effect only after ${override.variable} is removed.` }));
    } else {
      controlWrap.append(control);
    }
    if (schema.description) controlWrap.append(el("div", { class: "field-help", text: schema.description }));
    return el("div", { class: "field", "data-path": path.join(".") }, [label, controlWrap]);
  }

  function valueControl(path, schema, k) {
    const value = getAt(path);
    const id = "f-" + Math.random().toString(36).slice(2);

    if (k === "boolean" || k === "enum") {
      const options = k === "boolean" ? ["true", "false"] : schema.enum;
      const select = el("select", { id }, [el("option", { value: "", text: "(not set)" }), ...options.map((o) => el("option", { value: o, text: o }))]);
      select.value = value === undefined ? "" : String(value);
      if (value !== undefined && select.value !== String(value)) {
        select.append(el("option", { value: String(value), text: String(value) + " (current)" }));
        select.value = String(value);
      }
      select.addEventListener("change", () => {
        if (select.value === "") deleteAt(path);
        else setAt(path, k === "boolean" ? select.value === "true" : select.value);
      });
      return select;
    }

    if (k === "list" || k === "flex") {
      const lines = value === undefined ? "" : Array.isArray(value) ? value.join("\n") : String(value);
      const area = el("textarea", { id, rows: String(Math.min(8, Math.max(2, lines.split("\n").length + 1))), placeholder: "One value per line" });
      area.value = lines;
      area.addEventListener("change", () => {
        const items = area.value.split("\n").map((s) => s.trim()).filter(Boolean);
        if (items.length === 0) deleteAt(path);
        else if (k === "flex" && items.length === 1) setAt(path, items[0]);
        else setAt(path, items);
      });
      return area;
    }

    const input = el("input", { id, type: "text", autocomplete: "off", spellcheck: "false" });
    if (k === "integer" || k === "number") input.setAttribute("inputmode", k === "integer" ? "numeric" : "decimal");
    if (k === "duration") input.placeholder = "e.g. 30m, 6h, 2h45m";
    input.value = value === undefined ? "" : String(value);
    const error = el("div", { class: "field-error", hidden: "" });
    input.addEventListener("change", () => {
      const raw = input.value.trim();
      error.hidden = true;
      if (raw === "") return deleteAt(path);
      if (k === "integer" || k === "number") {
        const number = Number(raw);
        if (!Number.isFinite(number) || (k === "integer" && !Number.isInteger(number))) {
          error.textContent = k === "integer" ? "Enter a whole number." : "Enter a number.";
          error.hidden = false;
          return undefined;
        }
        return setAt(path, number);
      }
      return setAt(path, raw);
    });
    return el("div", {}, [input, error]);
  }

  function isPasswordHash(path) {
    return pathKey(path) === pathKey(["admin", "password_hash"]);
  }

  function secretControl(path, schema) {
    const wrap = el("div", { class: "secret" });
    const draw = () => {
      const value = getAt(path);
      const originalValue = (function () {
        let node = state.original;
        for (const segment of path) node = node && typeof node === "object" ? node[docKey(node, segment)] : undefined;
        return node;
      })();
      const status = value === undefined ? "Not set" : value === PLACEHOLDER ? "Set (hidden)" : "Changed; saved with the configuration";
      const buttons = [];
      buttons.push(button(value === undefined ? "Set" : "Replace", () => edit()));
      if (value !== undefined) buttons.push(button("Remove", () => { deleteAt(path); draw(); }));
      if (value !== originalValue && originalValue !== undefined) buttons.push(button("Undo", () => { setAt(path, clone(originalValue)); draw(); }));
      wrap.replaceChildren(el("span", { class: value === undefined ? "muted" : "secret-set", text: status }), ...buttons);
    };
    const edit = () => {
      if (isPasswordHash(path)) return editPassword();
      const multi = kind(schema) === "flex";
      const input = multi
        ? el("textarea", { rows: "3", placeholder: "One key per line; several keys are rotated", autocomplete: "off", spellcheck: "false" })
        : el("input", { type: "password", autocomplete: "new-password" });
      wrap.replaceChildren(input, button("Apply", () => {
        const items = input.value.split("\n").map((s) => s.trim()).filter(Boolean);
        if (items.length === 0) return;
        setAt(path, multi && items.length > 1 ? items : items[0]);
        draw();
      }), button("Cancel", draw));
      input.focus();
      return undefined;
    };
    const editPassword = () => {
      const first = el("input", { type: "password", autocomplete: "new-password", placeholder: "New password (12+ characters)" });
      const second = el("input", { type: "password", autocomplete: "new-password", placeholder: "Repeat password" });
      const error = el("div", { class: "field-error", hidden: "" });
      wrap.replaceChildren(first, second, button("Set password", async () => {
        error.hidden = true;
        if (first.value !== second.value) {
          error.textContent = "The passwords do not match.";
          error.hidden = false;
          return;
        }
        const response = await apiSend("POST", "api/password-hash", { password: first.value });
        if (!response.ok) {
          error.textContent = (response.data && response.data.error) || "Could not set the password.";
          error.hidden = false;
          return;
        }
        setAt(path, response.data.hash);
        draw();
      }), button("Cancel", draw), error);
      first.focus();
    };
    draw();
    return wrap;
  }

  function button(text, onClick, className) {
    const node = el("button", { class: "button small-button" + (className ? " " + className : ""), type: "button", text });
    node.addEventListener("click", onClick);
    return node;
  }

  function mapEditor(path, schema) {
    const entrySchema = schema.additionalProperties;
    const values = getAt(path) || {};
    const container = el("div", { class: "map" });
    const keys = Object.keys(values).sort((a, b) => a.localeCompare(b));
    if (keys.length === 0) container.append(el("p", { class: "muted", text: "None yet." }));

    for (const key of keys) {
      const entryPath = path.concat(key);
      const entry = values[key] || {};
      const url = entry[docKey(entry, "url")];
      const renameInput = el("input", { type: "text", value: key, "aria-label": "Rename " + key, class: "rename" });
      const actions = el("div", { class: "entry-actions" }, [
        renameInput,
        button("Rename", () => {
          const next = renameInput.value.trim();
          if (next === key) return;
          if (!FEED_ID.test(next)) return alert("IDs may only contain letters, digits, '-' and '_'.");
          if (Object.keys(values).some((k) => k.toLowerCase() === next.toLowerCase())) return alert(`"${next}" already exists.`);
          const moved = values[key];
          delete values[key];
          values[next] = moved;
          state.openPaths.add(pathKey(path.concat(next)));
          changed();
          render();
          return undefined;
        }),
        button("Remove", () => {
          if (!confirm(`Remove "${key}"? For feeds, downloaded episodes stay in storage.`)) return;
          deleteAt(entryPath);
          render();
        }, "danger"),
      ]);
      container.append(collapsible(entryPath,
        [el("span", { class: "entry-name", text: key }), url ? el("span", { class: "muted small", text: "  " + url }) : null],
        [actions, ...objectFields(entryPath, entrySchema)], "entry"));
    }

    const idInput = el("input", { type: "text", placeholder: path[path.length - 1] === "feeds" ? "new feed ID, e.g. my_show" : "new ID", class: "rename" });
    container.append(el("div", { class: "add-row" }, [idInput, button(path[path.length - 1] === "feeds" ? "Add feed" : "Add", () => {
      const id = idInput.value.trim();
      if (!FEED_ID.test(id)) return alert("IDs may only contain letters, digits, '-' and '_'.");
      if (Object.keys(values).some((k) => k.toLowerCase() === id.toLowerCase())) return alert(`"${id}" already exists.`);
      state.openPaths.add(pathKey(path));
      state.openPaths.add(pathKey(path.concat(id)));
      setAt(path.concat(id), {});
      render();
      return undefined;
    })]));
    return container;
  }

  function scalarMapEditor(path, schema) {
    const entrySchema = schema.additionalProperties || { type: "string" };
    const values = getAt(path) || {};
    const mapKey = pathKey(path);
    // Pending entries disappear once they have a value in the document.
    const pending = (state.pending[mapKey] || []).filter((name) => !Object.keys(values).some((k) => k.toLowerCase() === name));
    state.pending[mapKey] = pending;
    const keys = Object.keys(values).concat(pending).sort();

    const container = el("div", { class: "map" });
    if (keys.length === 0) container.append(el("p", { class: "muted", text: "None yet." }));
    for (const key of keys) {
      const entryPath = path.concat(key);
      const control = isSecret(entrySchema) ? secretControl(entryPath, entrySchema) : valueControl(entryPath, entrySchema, kind(entrySchema));
      const row = fieldRow(entryPath, key, { description: "" }, el("div", {}, [control, button("Remove " + key, () => {
        state.pending[mapKey] = (state.pending[mapKey] || []).filter((name) => name !== key);
        deleteAt(entryPath);
        render();
      }, "danger")]));
      container.append(row);
    }

    const suggestions = PROVIDERS.filter((p) => !keys.includes(p));
    const listId = "list-" + mapKey.replace(/[^a-z0-9]/g, "-");
    const nameInput = el("input", { type: "text", placeholder: suggestions.length ? "e.g. " + suggestions[0] : "name", class: "rename", list: listId });
    const datalist = el("datalist", { id: listId }, suggestions.map((p) => el("option", { value: p })));
    container.append(el("div", { class: "add-row" }, [nameInput, datalist, button("Add", () => {
      const name = nameInput.value.trim().toLowerCase();
      if (!FEED_ID.test(name)) return alert("Use a name such as youtube or vimeo.");
      if (keys.includes(name)) return alert(`"${name}" already exists.`);
      state.openPaths.add(mapKey);
      state.pending[mapKey] = (state.pending[mapKey] || []).concat(name);
      render();
      return undefined;
    })]));
    return container;
  }

  function objectListEditor(path, name, schema) {
    const items = getAt(path) || [];
    const list = el("div", { class: "object-list" });
    items.forEach((item, index) => {
      const itemPath = path.concat(index);
      const move = (delta) => {
        const target = index + delta;
        if (target < 0 || target >= items.length) return;
        const copy = items.slice();
        [copy[index], copy[target]] = [copy[target], copy[index]];
        setAt(path, copy);
        render();
      };
      list.append(el("fieldset", { class: "list-item" }, [
        el("legend", {}, [el("span", { text: `${name} #${index + 1}` })]),
        el("div", { class: "entry-actions" }, [
          button("Move up", () => move(-1)),
          button("Move down", () => move(1)),
          button("Remove", () => {
            deleteAt(itemPath);
            if ((getAt(path) || []).length === 0) deleteAt(path);
            render();
          }, "danger"),
        ]),
        ...objectFields(itemPath, schema.items),
      ]));
    });
    list.append(el("div", { class: "add-row" }, [button("Add " + name.replace(/s$/, "").replace(/_/g, " "), () => {
      const next = (getAt(path) || []).slice();
      next.push({});
      state.openPaths.add(pathKey(path));
      setAt(path, next);
      render();
    })]));
    return collapsible(path, [el("span", { class: "option-name", text: name }), el("span", { class: "muted small", text: ` ${items.length} item${items.length === 1 ? "" : "s"}` })],
      [schema.description ? el("div", { class: "field-help", text: schema.description }) : null, list], "subsection");
  }

  // ----- actions -----

  async function validate() {
    const container = document.getElementById("editor-messages");
    const response = await apiSend("POST", "api/config/validate", { document: state.doc });
    if (!response.ok) {
      message(container, "error", "Check failed", [(response.data && response.data.error) || `HTTP ${response.status}`]);
      return;
    }
    const result = response.data;
    showPreview(result.preview);
    if (result.valid) {
      const notes = (result.restart_required || []).map((s) => `Changes to [${s}] need a restart.`);
      message(container, "ok", "The configuration is valid.", notes);
    } else {
      message(container, "error", "The configuration has problems:", result.errors || []);
    }
  }

  async function save() {
    const container = document.getElementById("editor-messages");
    const saveButton = document.getElementById("editor-save");
    saveButton.disabled = true;
    try {
      const response = await apiSend("PUT", "api/config", { document: state.doc, version: state.snapshot.version });
      if (response.ok) {
        const result = response.data;
        const notes = [];
        if (result.backup) notes.push(`Previous version kept as ${result.backup}.`);
        if (result.feeds_added && result.feeds_added.length) notes.push(`Feeds added: ${result.feeds_added.join(", ")}.`);
        if (result.feeds_updated && result.feeds_updated.length) notes.push(`Feeds updated: ${result.feeds_updated.join(", ")}.`);
        if (result.feeds_removed && result.feeds_removed.length) notes.push(`Feeds removed: ${result.feeds_removed.join(", ")}.`);
        if (result.reload_error) notes.push(`Saved, but applying it reported: ${result.reload_error}`);
        await load(true);
        message(container, result.reload_error ? "warn" : "ok", "Saved and applied.", notes);
        if (typeof loadStatus === "function") loadStatus();
        return;
      }
      if (response.status === 409) {
        const reload = button("Load the current file (discards your changes)", async () => {
          await load(true);
          message(container, "ok", "Loaded the current file.", []);
        });
        message(container, "error", "The configuration file changed since you opened it (a hand edit or another admin). Nothing was saved.", [], reload);
        return;
      }
      const errors = (response.data && response.data.validation && response.data.validation.errors) || [(response.data && response.data.error) || `HTTP ${response.status}`];
      if (response.data && response.data.validation) showPreview(response.data.validation.preview);
      message(container, "error", "Nothing was saved. Fix these problems:", errors);
    } finally {
      changed();
    }
  }

  function discard() {
    if (!confirm("Discard all unsaved changes?")) return;
    state.doc = clone(state.original);
    state.pending = {};
    clearMessages();
    showPreview("");
    render();
    changed();
  }

  // ----- history -----

  async function showHistory() {
    const container = document.getElementById("history-messages");
    const body = document.getElementById("history").tBodies[0];
    try {
      const backups = await api("api/config/backups");
      if (backups.length === 0) {
        body.replaceChildren(el("tr", {}, [el("td", { colspan: "3", class: "muted", text: "No saved versions yet. Each save through this page keeps the previous file here." })]));
        return;
      }
      body.replaceChildren(...backups.map((backup) => el("tr", {}, [
        el("td", {}, [
          el("div", { text: exact(backup.created_at) + " (" + relative(backup.created_at) + ")" }),
          el("div", { class: "muted small", text: backup.name }),
        ]),
        el("td", { text: formatBytes(backup.size) }),
        el("td", {}, [button("Restore", () => restore(backup))]),
      ])));
    } catch (err) {
      message(container, "error", "Could not list saved versions", [err.message]);
    }
  }

  async function restore(backup) {
    const container = document.getElementById("history-messages");
    const warning = dirty() ? "\n\nYour unsaved changes in the Configuration tab will be discarded." : "";
    if (!confirm(`Restore the version from ${exact(backup.created_at)}? The current file is kept as a new saved version first.${warning}`)) return;
    const current = await api("api/config");
    const response = await apiSend("POST", `api/config/backups/${encodeURIComponent(backup.name)}/restore`, { version: current.version });
    if (response.ok) {
      await load(true);
      message(container, "ok", `Restored ${backup.name} and applied it.`, response.data.backup ? [`The replaced version was kept as ${response.data.backup}.`] : []);
      showHistory();
      if (typeof loadStatus === "function") loadStatus();
      return;
    }
    const errors = (response.data && response.data.validation && response.data.validation.errors) || [(response.data && response.data.error) || `HTTP ${response.status}`];
    message(container, "error", "The version was not restored.", errors);
  }

  // ----- wiring -----

  document.addEventListener("DOMContentLoaded", () => {
    document.getElementById("editor-validate").addEventListener("click", validate);
    document.getElementById("editor-save").addEventListener("click", save);
    document.getElementById("editor-discard").addEventListener("click", discard);
  });
  window.addEventListener("beforeunload", (event) => {
    if (dirty()) {
      event.preventDefault();
      event.returnValue = "";
    }
  });

  window.PodsyncEditor = {
    show: () => load(false),
    showHistory,
  };
})();
