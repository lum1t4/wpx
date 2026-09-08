(() => {
  const form = document.querySelector("[data-alert-settings]");
  if (!form) return;
  const master = form.querySelector("[data-alert-master]");
  const channels = [...form.querySelectorAll("[data-alert-channel]")];
  const persistedToggles = [...form.querySelectorAll("[data-alert-persist-toggle]")];
  const saveState = form.querySelector("[data-alert-save-state]");
  const csrf = form.querySelector('[name="csrf_token"]')?.value || "";
  const localized = (name, fallback) => form.querySelector(`[data-alert-label="${name}"]`)?.textContent || fallback;
  let submitting = false;
  let pendingToggles = 0;
  let uncertainToggle = false;
  const localToggleChanges = new Set();
  form.noValidate = true;
  master?.addEventListener("change", () => {
    const label = form.querySelector("[data-alert-master-label]");
    if (label) label.textContent = master.checked ? "On" : "Off";
  });

  const setRequired = (channel, required) => {
    channel.querySelectorAll("[data-channel-required]").forEach((field) => {
      field.required = required && field.dataset.secretConfigured !== "true";
    });
  };

  const reveal = (channel, requireConfiguration = false, focus = true) => {
    const enabled = channel.querySelector("[data-channel-enabled]");
    const fields = channel.querySelector("[data-channel-fields]");
    const configure = channel.querySelector("[data-channel-config-button]");
    if (!enabled || !fields || !configure) return;
    fields.hidden = false;
    configure.hidden = true;
    enabled.setAttribute("aria-expanded", "true");
    setRequired(channel, requireConfiguration);
    if (focus) fields.querySelector("input")?.focus();
  };

  channels.forEach((channel) => {
    const enabled = channel.querySelector("[data-channel-enabled]");
    const fields = channel.querySelector("[data-channel-fields]");
    const configure = channel.querySelector("[data-channel-config-button]");
    if (!enabled || !fields || !configure) return;

    configure.addEventListener("click", () => {
      reveal(channel, Boolean(master?.checked && enabled.checked));
    });
    enabled.addEventListener("change", () => {
      if (enabled.checked) {
        reveal(channel, Boolean(master?.checked));
      } else {
        fields.hidden = true;
        configure.hidden = false;
        enabled.setAttribute("aria-expanded", "false");
        setRequired(channel, false);
      }
    });
    master?.addEventListener("change", () => {
      setRequired(channel, Boolean(master.checked && enabled.checked));
    });
    setRequired(channel, false);
  });

  const controlsIn = (container) => [...container.querySelectorAll("input, select, textarea")]
    .filter((control) => !control.disabled && control.type !== "hidden");

  form.addEventListener("submit", (event) => {
    if (pendingToggles > 0 || uncertainToggle) {
      event.preventDefault();
      showSaveState(uncertainToggle ? "Reload to check the saved setting." : "Wait for the setting to finish saving.", uncertainToggle);
      return;
    }
    const submitter = event.submitter;
    const testing = submitter?.matches('[formaction="/alerts/test"][name="test_channel"]');
    const targetChannel = testing ? submitter.closest("[data-alert-channel]") : null;

    channels.forEach((channel) => {
      const enabled = channel.querySelector("[data-channel-enabled]");
      const required = testing
        ? channel === targetChannel
        : Boolean(enabled?.checked && (master?.checked || localToggleChanges.has(enabled.name)));
      setRequired(channel, required);
    });

    let controls;
    if (testing) {
      controls = targetChannel ? controlsIn(targetChannel) : [];
    } else {
      const shared = [...form.elements].filter((control) =>
        !control.disabled && control.type !== "hidden" && !control.closest("[data-alert-channel]"));
      const activeChannels = channels.filter((channel) => {
        const enabled = channel.querySelector("[data-channel-enabled]");
        return enabled?.checked && (master?.checked || localToggleChanges.has(enabled.name));
      });
      controls = shared.concat(activeChannels.flatMap(controlsIn));
    }

    const invalid = controls.find((control) => !control.checkValidity());
    if (!invalid) {
      submitting = true;
      return;
    }
    event.preventDefault();
    const invalidChannel = invalid.closest("[data-alert-channel]");
    if (invalidChannel) reveal(invalidChannel, true, false);
    invalid.reportValidity();
  });

  const saveButton = form.querySelector('button[type="submit"]:not([name="test_channel"]), button:not([type]):not([name="test_channel"])');
  form.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" || event.target.matches("button, textarea, select")) return;
    event.preventDefault();
    if (saveButton) form.requestSubmit(saveButton);
  });

  const explicitSnapshot = () => {
    const values = [];
    [...form.elements].forEach((control) => {
      if (!control.name || control.disabled || control.matches("[data-alert-persist-toggle]") || ["submit", "button"].includes(control.type)) return;
      if (["checkbox", "radio"].includes(control.type) && !control.checked) return;
      values.push([control.name, control.value]);
    });
    return JSON.stringify(values);
  };
  const savedExplicit = explicitSnapshot();
  const initialUnsaved = form.dataset.alertInitialUnsaved === "true";
  const hasUnsavedExplicitChanges = () => initialUnsaved || localToggleChanges.size > 0 || explicitSnapshot() !== savedExplicit;
  const showSaveState = (message, failed = false) => {
    if (!saveState) return;
    saveState.textContent = message;
    saveState.classList.toggle("text-red-700", failed);
    saveState.classList.toggle("dark:text-red-300", failed);
    saveState.classList.toggle("text-zinc-500", !failed);
    saveState.classList.toggle("dark:text-zinc-400", !failed);
  };
  const showEditState = () => showSaveState(hasUnsavedExplicitChanges() ? localized("unsaved", "Unsaved changes") : localized("saved", "All changes saved"));
  form.addEventListener("input", (event) => {
    if (!event.target.matches("[data-alert-persist-toggle]")) showEditState();
  });
  form.addEventListener("change", (event) => {
    if (!event.target.matches("[data-alert-persist-toggle]")) showEditState();
  });
  window.addEventListener("beforeunload", (event) => {
    if (submitting || (!hasUnsavedExplicitChanges() && pendingToggles === 0 && !uncertainToggle)) return;
    event.preventDefault();
    event.returnValue = "";
  });

  const confirmed = new Map(persistedToggles.map((toggle) => [toggle.name, toggle.checked]));
  const setToggleBusy = (busy) => {
    form.setAttribute("aria-busy", String(busy));
    persistedToggles.forEach((toggle) => { toggle.disabled = busy; });
    form.querySelectorAll("button").forEach((button) => { button.disabled = busy; });
  };
  const syncToggleUI = () => {
    const label = form.querySelector("[data-alert-master-label]");
    if (label && master) label.textContent = master.checked ? "On" : "Off";
    channels.forEach((channel) => {
      const enabled = channel.querySelector("[data-channel-enabled]");
      const fields = channel.querySelector("[data-channel-fields]");
      const configure = channel.querySelector("[data-channel-config-button]");
      if (!enabled || !fields || !configure) return;
      if (!enabled.checked) {
        fields.hidden = true;
        configure.hidden = false;
        enabled.setAttribute("aria-expanded", "false");
      }
      setRequired(channel, Boolean(master?.checked && enabled.checked && !fields.hidden));
    });
  };
  const applyToggleState = (state) => {
    persistedToggles.forEach((toggle) => {
      if (!localToggleChanges.has(toggle.name) && typeof state[toggle.name] === "boolean") toggle.checked = state[toggle.name];
    });
    syncToggleUI();
  };

  const channelHasConfiguration = (channel) => [...channel.querySelectorAll("[data-channel-required]")].every((field) =>
    field.dataset.secretConfigured === "true" || String(field.value || "").trim() !== "");

  class UncertainToggleError extends Error {}

  let toggleQueue = Promise.resolve();
  persistedToggles.forEach((toggle) => {
    toggle.addEventListener("change", () => {
      const desired = toggle.checked;
      const channel = toggle.closest("[data-alert-channel]");
      if (desired === confirmed.get(toggle.name) && localToggleChanges.has(toggle.name)) {
        localToggleChanges.delete(toggle.name);
        showEditState();
        return;
      }
      if (toggle === master && desired && channels.some((item) => {
        const enabled = item.querySelector("[data-channel-enabled]");
        return enabled?.checked && localToggleChanges.has(enabled.name);
      })) {
        localToggleChanges.add(toggle.name);
        showEditState();
        return;
      }
      if (desired && channel && !channelHasConfiguration(channel)) {
        localToggleChanges.add(toggle.name);
        reveal(channel, Boolean(master?.checked), false);
        showEditState();
        return;
      }
      pendingToggles++;
      setToggleBusy(true);
      toggleQueue = toggleQueue.then(async () => {
        showSaveState(localized("saving", "Saving…"));
        const controller = new AbortController();
        const timeout = window.setTimeout(() => controller.abort(), 10000);
        let responseWasSuccessful = false;
        try {
          const body = new URLSearchParams({ csrf_token: csrf, name: toggle.name, value: desired ? "yes" : "no" });
          const response = await fetch("/alerts/toggle", { method: "POST", headers: { "Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded" }, body, signal: controller.signal });
          responseWasSuccessful = response.ok;
          if (response.redirected) throw new Error("Session expired. Reload and sign in again.");
          const contentType = response.headers.get("content-type") || "";
          if (response.ok && !contentType.includes("application/json")) throw new UncertainToggleError("Could not confirm the change. Reload to check.");
          const result = contentType.includes("application/json") ? await response.json() : {};
          if (response.ok && (result?.ok !== true || !["enabled", "smtp_enabled", "slack_enabled", "telegram_enabled", "auto_swap"].every((name) => typeof result[name] === "boolean"))) {
            throw new UncertainToggleError("Could not confirm the change. Reload to check.");
          }
          if (!response.ok) {
            const fallback = response.status === 401 ? "Session expired. Reload and sign in again." : response.status === 403 ? "Permission denied." : "Could not save this setting.";
            throw new Error(result.error || fallback);
          }
          applyToggleState(result);
          persistedToggles.forEach((item) => {
            if (!localToggleChanges.has(item.name)) confirmed.set(item.name, item.checked);
          });
          localToggleChanges.delete(toggle.name);
          showEditState();
        } catch (error) {
          if (error instanceof UncertainToggleError || error.name === "AbortError" || error instanceof TypeError || (responseWasSuccessful && error instanceof SyntaxError)) {
            uncertainToggle = true;
            showSaveState("Could not confirm the change. Reload to check.", true);
          } else {
            toggle.checked = confirmed.get(toggle.name);
            syncToggleUI();
            showSaveState(error.message || "Could not save this setting. Change reverted.", true);
          }
        } finally {
          window.clearTimeout(timeout);
          pendingToggles--;
          if (pendingToggles === 0 && !uncertainToggle) setToggleBusy(false);
        }
      });
    });
  });
})();
