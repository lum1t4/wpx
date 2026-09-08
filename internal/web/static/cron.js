(() => {
  const dialog = document.querySelector("[data-cron-dialog]");
  if (!(dialog instanceof HTMLDialogElement)) return;
  let returnFocus = null;

  const custom = dialog.querySelector("[data-cron-custom]");
  const customInput = custom?.querySelector("input");
  const updateCustom = () => {
    const selected = dialog.querySelector('input[name="preset"]:checked');
    const visible = selected?.value === "custom";
    custom?.classList.toggle("hidden", !visible);
    if (customInput) customInput.required = visible;
  };

  document.querySelector("[data-cron-open]")?.addEventListener("click", (event) => {
    returnFocus = event.currentTarget;
    dialog.showModal();
  });
  dialog.querySelectorAll("[data-cron-close]").forEach((button) => {
    button.addEventListener("click", () => dialog.close());
  });
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) dialog.close();
  });
  dialog.addEventListener("close", () => returnFocus?.focus());
  dialog.querySelector("[data-cron-presets]")?.addEventListener("change", updateCustom);
  updateCustom();
  if (dialog.hasAttribute("data-open-on-load")) dialog.showModal();
})();
