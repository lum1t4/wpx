(() => {
  const pause = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));
  let pageGeneration = 0;
  let currentController;

  function cancelPending() {
    pageGeneration += 1;
    currentController?.abort();
    currentController = undefined;
  }

  function isCurrent(generation, node) {
    return generation === pageGeneration && document.contains(node);
  }

  function statusNode(form) {
    let node = form.querySelector("[data-action-status]");
    if (node) return node;
    node = document.createElement("p");
    node.dataset.actionStatus = "";
    node.setAttribute("role", "status");
    node.setAttribute("aria-live", "polite");
    node.className = "mt-3 text-sm text-zinc-600 dark:text-zinc-300";
    form.append(node);
    return node;
  }

  function showStatus(node, response) {
    node.textContent = "";
    node.className = response.status === "failed" ? "mt-3 text-sm text-red-700 dark:text-red-300" : "mt-3 text-sm text-zinc-600 dark:text-zinc-300";
    node.append(document.createTextNode(response.message));
    if (response.status === "queued" || response.status === "running") {
      const link = document.createElement("a");
      link.href = response.activity_url;
      link.className = "ml-2 font-medium underline underline-offset-4";
      link.textContent = "View Activity";
      node.append(link);
    }
  }

  async function requestJSON(url, options) {
    const response = await fetch(url, options);
    if (response.redirected) {
      throw new Error("Session expired. Reload and sign in again.");
    }
    if (!response.headers.get("content-type")?.includes("application/json")) {
      const text = await response.text();
      const document = new DOMParser().parseFromString(text, "text/html");
      const message = document.querySelector('[role="alert"]')?.textContent.trim() || text.trim();
      throw new Error(message || "The action could not be completed.");
    }
    return response.json();
  }

  async function refreshMain(url, generation, signal) {
    if (generation !== pageGeneration) return;
    const response = await fetch(url, { headers: { "X-WPX-Action": "refresh" }, signal });
    if (generation !== pageGeneration) return;
    if (response.redirected) {
      window.location.assign(response.url);
      return;
    }
    if (!response.ok) {
      window.location.assign(url);
      return;
    }
    const parsed = new DOMParser().parseFromString(await response.text(), "text/html");
    const next = parsed.querySelector("#main-content");
    const current = document.querySelector("#main-content");
    if (generation !== pageGeneration) return;
    if (!next || !current) {
      window.location.assign(url);
      return;
    }

    const assetURLs = (scope, selector) => Array.from(scope.querySelectorAll(selector), (element) => new URL(element.getAttribute("href") || element.getAttribute("src"), document.baseURI).href);
    const currentStyles = assetURLs(document, 'link[rel="stylesheet"][href]');
    const nextStyles = assetURLs(parsed, 'link[rel="stylesheet"][href]');
    const currentScripts = assetURLs(document.head, "script[src]");
    const nextScripts = assetURLs(parsed.head, "script[src]");
    const sameAssets = (left, right) => left.length === right.length && left.every((value, index) => value === right[index]);
    const stylesLoaded = Array.from(document.querySelectorAll('link[rel="stylesheet"][href]')).every((link) => link.sheet);
    // Scripts inserted with parsed markup do not execute. Pages with their own
    // initializer therefore use a normal navigation so their controls cannot
    // be replaced with inert copies.
    const hasPageScripts = Boolean(document.body.querySelector("script") || parsed.body.querySelector("script"));
    if (!stylesLoaded || hasPageScripts || !sameAssets(currentStyles, nextStyles) || !sameAssets(currentScripts, nextScripts)) {
      window.location.assign(url);
      return;
    }

    current.replaceWith(next);
    history.replaceState(null, "", url);
    next.dispatchEvent(new CustomEvent("wpx:content-updated", { bubbles: true }));
    if (!window.matchMedia("(prefers-reduced-motion: reduce)").matches && typeof next.animate === "function") {
      next.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 180, easing: "ease-out" });
    }
    const focusTarget = next.querySelector("h1") || next;
    if (!focusTarget.hasAttribute("tabindex")) focusTarget.setAttribute("tabindex", "-1");
    focusTarget.focus();
    pageGeneration += 1;
  }

  async function follow(response, node, generation, signal) {
    const expires = Date.now() + 12000;
    while (isCurrent(generation, node) && response && (response.status === "queued" || response.status === "running") && Date.now() < expires) {
      showStatus(node, response);
      await pause(450);
      if (!isCurrent(generation, node)) return response?.status;
      response = await requestJSON(response.poll_url, { headers: { "X-WPX-Action": "partial" }, signal });
    }
    if (!response || !isCurrent(generation, node)) return response?.status;
    showStatus(node, response);
    if (response.status === "succeeded") {
      await pause(180);
      if (isCurrent(generation, node)) await refreshMain(response.redirect_url, generation, signal);
    }
    return response.status;
  }

  window.addEventListener("pagehide", cancelPending);
  window.addEventListener("popstate", cancelPending);
  document.addEventListener("click", (event) => {
    const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
    if (link && !event.defaultPrevented && link.target !== "_blank" && !link.hasAttribute("download") && !link.getAttribute("href").startsWith("#")) {
      cancelPending();
    }
  }, true);

  document.addEventListener("submit", async (event) => {
    const form = event.target.closest("form[data-quick-action]");
    if (!form || event.defaultPrevented || !form.reportValidity()) return;
    event.preventDefault();
    cancelPending();
    const controller = new AbortController();
    currentController = controller;
    const generation = pageGeneration;
    const button = event.submitter || form.querySelector("button[type=submit], button:not([type]), input[type=submit]");
    const actionURL = button?.hasAttribute("formaction") ? button.formAction : form.action;
    const method = button?.hasAttribute("formmethod") ? button.formMethod : form.method;
    const formData = new FormData(form);
    if (button?.name) formData.append(button.name, button.value);
    const node = statusNode(form);
    if (button) button.disabled = true;
    const actionLabel = button?.dataset.actionLabel || form.dataset.actionLabel;
    node.textContent = actionLabel ? `${actionLabel}…` : "Working…";
    let keepDisabled = false;
    try {
      const response = await requestJSON(actionURL, {
        method: (method || "post").toUpperCase(),
        body: formData,
        headers: { "X-WPX-Action": "partial", "Accept": "application/json" },
        signal: controller.signal,
      });
      keepDisabled = response.status === "queued" || response.status === "running";
      const status = await follow(response, node, generation, controller.signal);
      keepDisabled = status === "queued" || status === "running";
    } catch (error) {
      if (error.name === "AbortError") return;
      node.className = "mt-3 text-sm text-red-700 dark:text-red-300";
      node.textContent = error.message || "The response could not be loaded. Check Activity before retrying.";
    } finally {
      if (currentController === controller) currentController = undefined;
      if (button && document.contains(button) && !keepDisabled) button.disabled = false;
    }
  });
})();
