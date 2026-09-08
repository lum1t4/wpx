(() => {
  "use strict";

  const storageKey = "wpx-theme";
  const choices = new Set(["system", "light", "dark"]);
  const root = document.documentElement;
  const systemTheme = window.matchMedia("(prefers-color-scheme: dark)");

  function storedPreference() {
    try {
      const value = window.localStorage.getItem(storageKey);
      return choices.has(value) ? value : "system";
    } catch (_) {
      return "system";
    }
  }

  let preference = storedPreference();

  function resolvedTheme() {
    if (preference === "system") return systemTheme.matches ? "dark" : "light";
    return preference;
  }

  function applyTheme() {
    const theme = resolvedTheme();
    root.dataset.theme = theme;
    root.dataset.themePreference = preference;
    root.style.colorScheme = theme;
  }

  function syncChoices(scope = document) {
    scope.querySelectorAll("input[type=radio][data-theme-choice]").forEach((control) => {
      control.checked = control.value === preference;
    });
  }

  function announceChoice() {
    const status = document.querySelector("[data-theme-status]");
    if (!status) return;
    const label = preference === "system" ? `System (${resolvedTheme()})` : preference[0].toUpperCase() + preference.slice(1);
    status.textContent = `Theme set to ${label}.`;
  }

  function selectPreference(value) {
    if (!choices.has(value)) return;
    preference = value;
    try {
      window.localStorage.setItem(storageKey, preference);
    } catch (_) {
      // Storage may be unavailable in hardened browsing modes. The selected
      // theme still applies for the lifetime of this page.
    }
    applyTheme();
    syncChoices();
    announceChoice();
  }

  // This runs while the blocking head script is evaluated, before page content
  // paints. The dark variant therefore matches on the first rendered frame.
  applyTheme();

  document.addEventListener("change", (event) => {
    const control = event.target instanceof Element ? event.target.closest("input[type=radio][data-theme-choice]") : null;
    if (control?.checked) selectPreference(control.value);
  });

  const followSystem = () => {
    if (preference === "system") applyTheme();
  };
  if (typeof systemTheme.addEventListener === "function") systemTheme.addEventListener("change", followSystem);
  else if (typeof systemTheme.addListener === "function") systemTheme.addListener(followSystem);

  window.addEventListener("storage", (event) => {
    if (event.key !== storageKey) return;
    preference = choices.has(event.newValue) ? event.newValue : "system";
    applyTheme();
    syncChoices();
  });

  document.addEventListener("wpx:content-updated", (event) => syncChoices(event.target));
  document.addEventListener("DOMContentLoaded", () => {
    syncChoices();
    const observer = new MutationObserver((mutations) => {
      for (const mutation of mutations) {
        for (const node of mutation.addedNodes) {
          if (!(node instanceof Element)) continue;
          if (node.matches("input[type=radio][data-theme-choice]")) syncChoices(node.parentElement || node);
          else if (node.querySelector("input[type=radio][data-theme-choice]")) syncChoices(node);
        }
      }
    });
    observer.observe(document.body, { childList: true, subtree: true });
  });
})();
