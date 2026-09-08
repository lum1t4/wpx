(() => {
  const page = document.querySelector("[data-fleet-page]");
  if (!page) return;

  const search = page.querySelector("[data-fleet-search]");
  const updatesOnly = page.querySelector("[data-fleet-updates-only]");
  const sites = [...page.querySelectorAll("[data-fleet-site]")];
  const empty = page.querySelector("[data-fleet-empty]");
  let refreshSelectVisible = () => {};

  const activateTab = (tabs, name) => {
    tabs.querySelectorAll("[data-fleet-tab]").forEach((button) => {
      const active = button.dataset.fleetTab === name;
      button.setAttribute("aria-selected", String(active));
      button.tabIndex = active ? 0 : -1;
      button.classList.toggle("border-zinc-950", active);
      button.classList.toggle("dark:border-zinc-100", active);
      button.classList.toggle("text-zinc-950", active);
      button.classList.toggle("dark:text-zinc-100", active);
      button.classList.toggle("border-transparent", !active);
      button.classList.toggle("text-zinc-500", !active);
      button.classList.toggle("dark:text-zinc-400", !active);
    });
    tabs.querySelectorAll("[data-fleet-panel]").forEach((panel) => {
      panel.hidden = panel.dataset.fleetPanel !== name;
    });
    refreshSelectVisible();
  };

  page.querySelectorAll("[data-fleet-tabs]").forEach((tabs) => {
    const buttons = [...tabs.querySelectorAll("[data-fleet-tab]")];
    buttons.forEach((button) => {
      button.addEventListener("click", () => activateTab(tabs, button.dataset.fleetTab));
      button.addEventListener("keydown", (event) => {
        const index = buttons.indexOf(button);
        let next = index;
        if (event.key === "ArrowRight") next = (index + 1) % buttons.length;
        else if (event.key === "ArrowLeft") next = (index - 1 + buttons.length) % buttons.length;
        else if (event.key === "Home") next = 0;
        else if (event.key === "End") next = buttons.length - 1;
        else return;
        event.preventDefault();
        activateTab(tabs, buttons[next].dataset.fleetTab);
        buttons[next].focus();
      });
    });
    activateTab(tabs, "plugins");
  });

  const applyFilters = () => {
    const query = (search?.value || "").trim().toLocaleLowerCase();
    const onlyUpdates = Boolean(updatesOnly?.checked);
    let visible = 0;
    sites.forEach((site) => {
      const matchesSearch = !query || (site.dataset.siteSearch || "").toLocaleLowerCase().includes(query);
      const matchesUpdates = !onlyUpdates || site.dataset.hasUpdate === "true";
      site.hidden = !(matchesSearch && matchesUpdates);
      if (site.hidden) return;
      visible += 1;

      site.querySelectorAll("[data-component-row]").forEach((row) => {
        row.hidden = onlyUpdates && row.dataset.hasUpdate !== "true";
      });
      if (onlyUpdates) {
        const tabs = site.querySelector("[data-fleet-tabs]");
        const pluginUpdates = site.querySelector('[data-fleet-panel="plugins"] [data-component-row][data-has-update="true"]');
        const themeUpdates = site.querySelector('[data-fleet-panel="themes"] [data-component-row][data-has-update="true"]');
        if (tabs && !pluginUpdates && themeUpdates) activateTab(tabs, "themes");
      }
    });
    if (empty) empty.classList.toggle("hidden", visible !== 0);
    refreshSelectVisible();
  };

  search?.addEventListener("input", applyFilters);
  updatesOnly?.addEventListener("change", applyFilters);

  const form = page.querySelector("[data-fleet-form]");
  const selection = page.querySelector("[data-fleet-selection]");
  const count = page.querySelector("[data-fleet-selection-count]");
  const storage = page.querySelector("[data-fleet-storage]");
  const selectVisible = page.querySelector("[data-fleet-select-visible]");
  const clear = page.querySelector("[data-fleet-clear]");
  const updates = [...page.querySelectorAll("[data-fleet-update]")];
  const visibleUpdates = () => updates.filter((input) => {
    const site = input.closest("[data-fleet-site]");
    const row = input.closest("[data-component-row]");
    const panel = input.closest("[data-fleet-panel]");
    return site && !site.hidden && row && !row.hidden && panel && !panel.hidden && !input.disabled;
  });
  const updateSelection = () => {
    const selected = updates.filter((input) => input.checked).length;
    if (count) count.textContent = String(selected);
    if (selection) selection.hidden = selected === 0;
    if (storage) storage.disabled = selected === 0;
  };
  refreshSelectVisible = () => {
    if (selectVisible) selectVisible.disabled = visibleUpdates().length === 0;
  };
  updates.forEach((input) => input.addEventListener("change", updateSelection));
  selectVisible?.addEventListener("click", () => {
    visibleUpdates().forEach((input) => { input.checked = true; });
    updateSelection();
  });
  clear?.addEventListener("click", () => {
    updates.forEach((input) => { input.checked = false; });
    updateSelection();
  });
  form?.addEventListener("submit", (event) => {
    if (!updates.some((input) => input.checked)) event.preventDefault();
  });
  updateSelection();
  applyFilters();
})();
