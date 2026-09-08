(() => {
  const dialog = document.querySelector("[data-logs-filter-dialog]");
  let returnFocus = null;

  if (dialog instanceof HTMLDialogElement) {
    document.querySelector("[data-logs-filter-open]")?.addEventListener("click", (event) => {
      returnFocus = event.currentTarget;
      dialog.showModal();
    });
    dialog.querySelectorAll("[data-logs-filter-close]").forEach((button) => {
      button.addEventListener("click", () => dialog.close());
    });
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) dialog.close();
    });
    dialog.addEventListener("close", () => returnFocus?.focus());
    if (dialog.hasAttribute("data-open-on-load")) dialog.showModal();
  }

  const copyButton = document.querySelector("[data-logs-copy]");
  const copyStatus = document.querySelector("[data-logs-copy-status]");
  const table = document.querySelector("[data-request-log-table]");
  if (!(copyButton instanceof HTMLButtonElement) || !(copyStatus instanceof HTMLElement) || !(table instanceof HTMLTableElement)) return;

  const writeText = async (value) => {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
      return;
    }
    const textarea = document.createElement("textarea");
    textarea.value = value;
    textarea.setAttribute("readonly", "");
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    document.body.append(textarea);
    textarea.select();
    const copied = document.execCommand("copy");
    textarea.remove();
    if (!copied) throw new Error("copy failed");
  };

  copyButton.addEventListener("click", async () => {
    const header = Array.from(table.querySelectorAll("thead th"), (cell) => cell.textContent.trim()).join("\t");
    const rows = Array.from(table.querySelectorAll("[data-request-log-row]"), (row) =>
      Array.from(row.cells, (cell) => cell.textContent.trim()).join("\t")
    );
    try {
      await writeText([header, ...rows].join("\n"));
      copyStatus.textContent = `Copied ${rows.length} request${rows.length === 1 ? "" : "s"}.`;
    } catch {
      copyStatus.textContent = "Could not copy requests.";
    }
    copyStatus.classList.remove("hidden");
  });
})();
