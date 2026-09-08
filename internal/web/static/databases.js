(() => {
  const dialog = document.querySelector("[data-database-create-dialog]");
  if (dialog instanceof HTMLDialogElement) {
    let returnFocus = null;
    document.querySelectorAll("[data-database-create-open]").forEach((button) => {
      button.addEventListener("click", (event) => {
        event.preventDefault();
        returnFocus = button;
        dialog.showModal();
      });
    });
    dialog.querySelectorAll("[data-database-create-close]").forEach((button) => {
      button.addEventListener("click", () => dialog.close());
    });
    dialog.addEventListener("click", (event) => {
      if (event.target !== dialog) return;
      const box = dialog.getBoundingClientRect();
      if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) dialog.close();
    });
    dialog.addEventListener("close", () => returnFocus?.focus());
    const openForLocation = () => {
      if (!dialog.open && (dialog.hasAttribute("data-open-on-load") || location.hash === `#${dialog.id}`)) dialog.showModal();
    };
    window.addEventListener("hashchange", openForLocation);
    openForLocation();
  }

  document.querySelectorAll("[data-database-delete-open]").forEach((button) => {
    const target = document.getElementById(button.dataset.databaseDeleteOpen || "");
    if (!(target instanceof HTMLDialogElement)) return;
    button.addEventListener("click", () => target.showModal());
    target.querySelectorAll("[data-database-delete-close]").forEach((close) => {
      close.addEventListener("click", () => target.close());
    });
    target.addEventListener("click", (event) => {
      if (event.target !== target) return;
      const box = target.getBoundingClientRect();
      if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) target.close();
    });
    target.addEventListener("close", () => button.focus());
  });
})();
