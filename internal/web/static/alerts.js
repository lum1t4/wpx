(() => {
  const form = document.querySelector("[data-alert-settings]");
  if (!form) return;
  const master = form.querySelector("[data-alert-master]");
  const channels = [...form.querySelectorAll("[data-alert-channel]")];
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
    const submitter = event.submitter;
    const testing = submitter?.matches('[formaction="/alerts/test"][name="test_channel"]');
    const targetChannel = testing ? submitter.closest("[data-alert-channel]") : null;

    channels.forEach((channel) => {
      const enabled = channel.querySelector("[data-channel-enabled]");
      const required = testing
        ? channel === targetChannel
        : Boolean(master?.checked && enabled?.checked);
      setRequired(channel, required);
    });

    let controls;
    if (testing) {
      controls = targetChannel ? controlsIn(targetChannel) : [];
    } else {
      const shared = [...form.elements].filter((control) =>
        !control.disabled && control.type !== "hidden" && !control.closest("[data-alert-channel]"));
      const activeChannels = master?.checked
        ? channels.filter((channel) => channel.querySelector("[data-channel-enabled]")?.checked)
        : [];
      controls = shared.concat(activeChannels.flatMap(controlsIn));
    }

    const invalid = controls.find((control) => !control.checkValidity());
    if (!invalid) return;
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
})();
