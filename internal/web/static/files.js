(() => {
  "use strict";
  const root = document.getElementById("file-manager");
  if (!root) return;

  const site = root.dataset.siteId;
  const directory = root.dataset.directory || "";
  const csrf = root.dataset.csrf;
  const apiBase = `/sites/${encodeURIComponent(site)}/files/api`;
  const body = document.getElementById("files-table-body");
  const selectAll = document.getElementById("select-all-files");
  const selectionLabel = document.getElementById("files-selection-count");
  const selectionToolbar = document.getElementById("files-selection-toolbar");
  const moreActions = document.getElementById("files-more-actions");
  const notice = document.getElementById("files-notice");
  const uploads = new Map();
  const chunkBytes = 512 * 1024;

  const rows = () => Array.from(body.querySelectorAll("[data-file-row]"));
  const selectedRows = () => rows().filter(row => row.querySelector("[data-file-select]").checked);
  const selectedPaths = () => selectedRows().map(row => row.dataset.path);
  const action = name => root.querySelector(`[data-action="${name}"]`);
  const joinPath = (left, right) => left ? `${left}/${right}` : right;
  const fileURL = path => `${apiBase}/download?path=${encodeURIComponent(path)}`;

  function showNotice(message, error = false) {
    notice.textContent = message;
    notice.className = `rounded-lg border p-4 text-sm ${error ? "border-red-200 dark:border-red-900 bg-red-50 dark:bg-red-950 text-red-700 dark:text-red-300" : "border-emerald-200 dark:border-emerald-900 bg-emerald-50 dark:bg-emerald-950 text-emerald-800 dark:text-emerald-200"}`;
  }

  function updateActions() {
    const selected = selectedRows();
    const count = selected.length;
    selectionLabel.textContent = String(count);
    selectionToolbar.hidden = count === 0;
    action("download").hidden = count !== 1 || selected[0]?.dataset.directory === "true";
    action("rename").hidden = count !== 1;
    action("extract").hidden = count !== 1 || selected[0]?.dataset.directory === "true";
    action("paste").hidden = !sessionStorage.getItem(`wpx-files-clipboard:${site}`);
    ["copy", "cut", "archive", "delete", "rename", "download", "extract", "paste"].forEach(name => action(name).disabled = false);
    action("mkdir").disabled = false;
    if (!count) moreActions.open = false;
    selectAll.checked = count > 0 && count === rows().length;
    selectAll.indeterminate = count > 0 && count < rows().length;
  }

  root.addEventListener("change", event => {
    if (event.target.matches("[data-file-select]")) updateActions();
  });
  selectAll.addEventListener("change", () => {
    rows().forEach(row => row.querySelector("[data-file-select]").checked = selectAll.checked);
    updateActions();
  });
  document.getElementById("clear-file-selection").addEventListener("click", () => {
    rows().forEach(row => row.querySelector("[data-file-select]").checked = false);
    updateActions();
  });

  async function jsonRequest(endpoint, payload) {
    const response = await fetch(`${apiBase}/${endpoint}`, {
      method: "POST", credentials: "same-origin",
      headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf},
      body: JSON.stringify(payload)
    });
    const result = await responseJSON(response);
    if (!response.ok) throw new Error(result.error || `Request failed (${response.status})`);
    return result;
  }

  async function responseJSON(response) {
    if (response.redirected) {
      throw new Error("Session expired. Reload and sign in again.");
    }
    if (!response.headers.get("Content-Type")?.toLowerCase().includes("application/json")) {
      if (response.status === 401 || response.status === 403) throw new Error("You do not have permission for this file operation.");
      throw new Error(`The server returned an unavailable response (${response.status}). Try again.`);
    }
    try { return await response.json(); }
    catch (_) { throw new Error("The server returned an invalid response. Try again."); }
  }

  async function mutate(endpoint, payload, message) {
    try {
      root.querySelectorAll("[data-action]").forEach(control => control.disabled = true);
      showNotice("Working… Keep this page open until the operation finishes.");
      await jsonRequest(endpoint, payload);
      showNotice(message);
      window.location.reload();
    } catch (error) {
      showNotice(error.message, true);
      updateActions();
    }
  }

  action("download").addEventListener("click", () => { window.location.assign(fileURL(selectedPaths()[0])); });
  action("mkdir").addEventListener("click", () => {
    const name = window.prompt("Folder name");
    if (name) mutate("mkdir", {path: joinPath(directory, name)}, "Folder created.");
  });
  action("rename").addEventListener("click", () => {
    const row = selectedRows()[0];
    const name = window.prompt("New name", row.dataset.name);
    if (name && name !== row.dataset.name) mutate("rename", {path: row.dataset.path, new_name: name}, "Renamed.");
  });
  action("delete").addEventListener("click", () => {
    const paths = selectedPaths();
    if (window.confirm(`Permanently delete ${paths.length} selected item${paths.length === 1 ? "" : "s"}?`)) mutate("delete", {paths}, "Deleted.");
  });
  action("archive").addEventListener("click", () => {
    const suggested = joinPath(directory, "archive.zip");
    const destination = window.prompt("Archive path (.zip)", suggested);
    if (destination) mutate("archive", {paths: selectedPaths(), destination}, "Archive created.");
  });
  action("extract").addEventListener("click", () => {
    const destination = window.prompt("Extract into directory", directory);
    if (destination !== null) mutate("extract", {path: selectedPaths()[0], destination}, "Archive extracted.");
  });
  ["copy", "cut"].forEach(mode => action(mode).addEventListener("click", () => {
    sessionStorage.setItem(`wpx-files-clipboard:${site}`, JSON.stringify({mode, paths: selectedPaths()}));
    showNotice(`${selectedPaths().length} item${selectedPaths().length === 1 ? "" : "s"} ready to ${mode === "cut" ? "move" : "copy"}. Open a destination and choose Paste.`);
    moreActions.open = false;
    updateActions();
  }));
  action("paste").addEventListener("click", async () => {
    let clipboard;
    try { clipboard = JSON.parse(sessionStorage.getItem(`wpx-files-clipboard:${site}`)); } catch (_) { return; }
    if (!clipboard?.paths?.length) return;
    const overwrite = window.confirm("Replace destination items with matching names? Choose Cancel to keep existing items and report conflicts.");
    try {
      await jsonRequest(clipboard.mode === "cut" ? "move" : "copy", {paths: clipboard.paths, destination: directory, overwrite});
      if (clipboard.mode === "cut") sessionStorage.removeItem(`wpx-files-clipboard:${site}`);
      window.location.reload();
    } catch (error) { showNotice(error.message, true); }
  });
  document.getElementById("refresh-files").addEventListener("click", () => window.location.reload());

  let searchTimer;
  let searchController;
  document.getElementById("files-search").addEventListener("input", event => {
    clearTimeout(searchTimer);
    searchController?.abort();
    const query = event.target.value.trim();
    if (!query) { window.location.reload(); return; }
    searchTimer = setTimeout(async () => {
      try {
        searchController = new AbortController();
        const response = await fetch(`${apiBase}/search?path=${encodeURIComponent(directory)}&q=${encodeURIComponent(query)}`, {credentials: "same-origin", signal: searchController.signal});
        const result = await responseJSON(response);
        if (!response.ok) throw new Error(result.error || "Search failed");
        renderSearch(result.entries || [], result.truncated);
      } catch (error) { if (error.name !== "AbortError") showNotice(error.message, true); }
    }, 250);
  });

  function renderSearch(entries, truncated) {
    body.replaceChildren();
    for (const entry of entries) {
      const row = document.createElement("tr");
      row.dataset.fileRow = ""; row.dataset.path = entry.path; row.dataset.name = entry.name; row.dataset.directory = String(entry.is_dir);
      row.className = "hover:bg-zinc-50 dark:hover:bg-zinc-800";
      const checkCell = document.createElement("td"); checkCell.className = "px-4 py-3";
      const check = document.createElement("input"); check.type = "checkbox"; check.value = entry.path; check.dataset.fileSelect = ""; check.setAttribute("aria-label", `Select ${entry.name}`); check.className = "size-4 rounded border-zinc-300 dark:border-zinc-700"; checkCell.append(check);
      const nameCell = document.createElement("td"); nameCell.className = "px-2 py-3";
      const link = document.createElement("a"); link.className = "flex min-w-0 items-center gap-2 truncate hover:underline"; link.href = entry.is_dir ? `/sites/${encodeURIComponent(site)}/files?path=${encodeURIComponent(entry.path)}` : `/sites/${encodeURIComponent(site)}/files?path=${encodeURIComponent(parentPath(entry.path))}&edit=${encodeURIComponent(entry.path)}`;
      if (entry.is_dir) link.append(folderIcon());
      const label = document.createElement("span"); label.className = "truncate"; label.textContent = entry.path; link.append(label); nameCell.append(link);
      const size = document.createElement("td"); size.className = "px-3 py-3 text-xs text-zinc-500 dark:text-zinc-400"; size.textContent = entry.is_dir ? "—" : formatBytes(entry.size);
      row.append(checkCell, nameCell, size); body.append(row);
    }
    if (!entries.length) { const row = document.createElement("tr"); row.innerHTML = '<td colspan="3" class="px-5 py-10 text-center text-sm text-zinc-500 dark:text-zinc-400">No matching files.</td>'; body.append(row); }
    if (truncated) showNotice("Showing the first 200 matches. Refine your search.");
    updateActions();
  }

  function folderIcon() {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 24 24"); svg.setAttribute("fill", "none"); svg.setAttribute("stroke", "currentColor"); svg.setAttribute("stroke-width", "1.6"); svg.setAttribute("aria-hidden", "true"); svg.classList.add("size-4", "shrink-0", "text-zinc-400");
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path"); path.setAttribute("d", "M3 7V5a1 1 0 0 1 1-1h5l2 3h9a1 1 0 0 1 1 1v11a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V7Z"); path.setAttribute("stroke-linejoin", "round"); svg.append(path);
    return svg;
  }

  const parentPath = path => path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "";
  function formatBytes(value) {
    if (!value) return "0 B";
    const units = ["B", "KiB", "MiB", "GiB", "TiB"];
    const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
    return `${(value / (1024 ** index)).toFixed(index ? 1 : 0)} ${units[index]}`;
  }

  const uploadPanel = document.getElementById("upload-panel");
  const uploadList = document.getElementById("upload-list");
  const resumeKey = `wpx-file-uploads:${site}`;
  document.getElementById("files-upload-input").addEventListener("change", async event => {
    for (const file of Array.from(event.target.files)) {
      const fingerprint = await fileFingerprint(file);
      const pending = Array.from(uploads.values()).find(state => !state.file && state.path === joinPath(directory, file.name) && state.size === file.size && state.fingerprint === fingerprint);
      if (pending) { pending.file = file; pending.status = "uploading"; renderUpload(pending); sendUpload(pending); }
      else queueUpload(file, fingerprint);
    }
    event.target.value = "";
  });

  function uploadID() {
    const data = new Uint8Array(18); crypto.getRandomValues(data);
    return Array.from(data, byte => byte.toString(16).padStart(2, "0")).join("");
  }
  async function fileFingerprint(file) {
    const sampleBytes = 64 * 1024;
    const first = new Uint8Array(await file.slice(0, sampleBytes).arrayBuffer());
    const last = new Uint8Array(await file.slice(Math.max(first.length, file.size - sampleBytes)).arrayBuffer());
    const metadata = new TextEncoder().encode(`${file.name}\0${file.size}`);
    const sample = new Uint8Array(metadata.length + first.length + last.length);
    sample.set(metadata); sample.set(first, metadata.length); sample.set(last, metadata.length + first.length);
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", sample));
    return Array.from(digest, byte => byte.toString(16).padStart(2, "0")).join("");
  }
  function queueUpload(file, fingerprint) {
    const destination = joinPath(directory, file.name);
    const exists = rows().some(row => row.dataset.path === destination);
    if (exists && !window.confirm(`${file.name} already exists. Replace it?`)) return;
    const state = {id: uploadID(), file, path: destination, size: file.size, fingerprint, offset: 0, overwrite: exists, status: "uploading", controller: null};
    uploads.set(state.id, state); persistUploads(); renderUpload(state); sendUpload(state);
  }
  function persistUploads() {
    const pending = Array.from(uploads.values()).filter(state => state.status !== "complete").map(({id, path, size, fingerprint, offset, overwrite}) => ({id, path, size, fingerprint, offset, overwrite}));
    if (pending.length) localStorage.setItem(resumeKey, JSON.stringify(pending)); else localStorage.removeItem(resumeKey);
  }
  function renderUpload(state) {
    uploadPanel.classList.remove("hidden");
    let row = uploadList.querySelector(`[data-upload-id="${state.id}"]`);
    if (!row) { row = document.createElement("div"); row.dataset.uploadId = state.id; row.className = "grid gap-2 sm:grid-cols-[minmax(0,1fr)_8rem_auto] sm:items-center"; uploadList.append(row); }
    const percent = state.size ? Math.floor(state.offset / state.size * 100) : (state.status === "complete" ? 100 : 0);
    row.replaceChildren();
    const info = document.createElement("div"); info.className = "min-w-0"; const title = document.createElement("p"); title.className = "truncate text-sm font-medium"; title.textContent = state.path; const bar = document.createElement("div"); bar.className = "mt-1 h-1.5 overflow-hidden rounded-full bg-zinc-100 dark:bg-zinc-800"; const fill = document.createElement("div"); fill.className = "h-full bg-zinc-900"; fill.style.width = `${percent}%`; bar.append(fill); info.append(title, bar);
    const status = document.createElement("p"); status.className = "text-xs tabular-nums text-zinc-500 dark:text-zinc-400"; status.textContent = !state.file ? `Reselect file (${percent}%)` : state.status === "error" ? `Paused at ${percent}%` : state.status === "paused" ? `Paused at ${percent}%` : state.status === "complete" ? "Complete" : `${percent}%`;
    const button = document.createElement("button"); button.className = "rounded-md border border-zinc-300 dark:border-zinc-700 px-3 py-1.5 text-xs font-medium"; button.textContent = state.status === "uploading" ? "Pause" : state.status === "complete" ? "Remove" : "Resume";
    button.disabled = !state.file && state.status !== "complete"; button.addEventListener("click", () => { if (state.status === "uploading") { state.status = "paused"; state.controller?.abort(); persistUploads(); renderUpload(state); } else if (state.status === "complete") { uploads.delete(state.id); persistUploads(); row.remove(); if (!uploads.size) uploadPanel.classList.add("hidden"); } else { state.status = "uploading"; renderUpload(state); sendUpload(state); } });
    row.append(info, status, button);
  }
  async function sendUpload(state) {
    while (state.status === "uploading") {
      const end = Math.min(state.offset + chunkBytes, state.size);
      const final = end === state.size;
      state.controller = new AbortController();
      try {
        const response = await fetch(`${apiBase}/upload?path=${encodeURIComponent(state.path)}&upload_id=${encodeURIComponent(state.id)}&offset=${state.offset}&final=${final ? "1" : "0"}&overwrite=${state.overwrite ? "1" : "0"}`, {method: "POST", credentials: "same-origin", headers: {"Content-Type": "application/octet-stream", "X-CSRF-Token": csrf}, body: state.file.slice(state.offset, end), signal: state.controller.signal});
        const result = await responseJSON(response);
        if (!response.ok) throw new Error(result.error || `Upload failed (${response.status})`);
        state.offset = result.next_offset;
        persistUploads();
        if (result.complete) { state.status = "complete"; persistUploads(); renderUpload(state); showNotice(`${state.path} uploaded. Refresh the directory to see it.`); return; }
        renderUpload(state);
      } catch (error) {
        if (state.status !== "paused") { state.status = "error"; state.error = error.message; showNotice(`${state.path}: ${error.message}. Resume continues from the confirmed offset.`, true); }
        persistUploads();
        renderUpload(state); return;
      }
    }
  }
  document.getElementById("clear-complete-uploads").addEventListener("click", () => { for (const [id, state] of uploads) if (state.status === "complete") { uploadList.querySelector(`[data-upload-id="${id}"]`)?.remove(); uploads.delete(id); } if (!uploads.size) uploadPanel.classList.add("hidden"); });
  try {
    for (const saved of JSON.parse(localStorage.getItem(resumeKey) || "[]")) {
      const state = {...saved, file: null, status: "paused", controller: null}; uploads.set(state.id, state); renderUpload(state);
    }
    if (uploads.size) showNotice("Reselect the matching file to resume each interrupted upload from its confirmed offset.");
  } catch (_) { localStorage.removeItem(resumeKey); }
  updateActions();
})();
