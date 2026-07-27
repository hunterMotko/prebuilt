// Nav — scroll effect and mobile toggle.
//
// Guarded because error.html loads this file but has no nav at all: without
// the check, `toggle.addEventListener` threw a TypeError on null at parse
// time and killed the ENTIRE script, so every block below this one silently
// stopped running on that page. Same null-guard pattern as the gallery and
// lightbox sections further down.
const nav = document.getElementById('nav');
const toggle = document.getElementById('navToggle');
const links = document.getElementById('navLinks');

if (nav) {
	window.addEventListener('scroll', () => {
		nav.classList.toggle('scrolled', window.scrollY > 40);
	}, { passive: true });
}

// Kept in one setNavOpen() so every way of closing the menu (link click,
// Escape, clicking outside, widening past the breakpoint) leaves the panel,
// the hamburger icon, and aria-expanded in agreement — previously each close
// path updated its own subset and they could drift.
if (toggle && links) {
	const setNavOpen = (open) => {
		links.classList.toggle('open', open);
		toggle.classList.toggle('open', open);
		toggle.setAttribute('aria-expanded', open);
	};

	toggle.addEventListener('click', () => {
		setNavOpen(!links.classList.contains('open'));
	});

	// Close on link click
	links.querySelectorAll('a').forEach(a => {
		a.addEventListener('click', () => setNavOpen(false));
	});

	// Escape closes it, and focus goes back to the hamburger so a keyboard user
	// isn't stranded on a link that just became unfocusable.
	document.addEventListener('keydown', e => {
		if (e.key === 'Escape' && links.classList.contains('open')) {
			setNavOpen(false);
			toggle.focus();
		}
	});

	// Clicking anywhere outside the menu closes it — expected behavior for a
	// dropdown, and without it the only way out was the hamburger itself.
	document.addEventListener('click', e => {
		if (!links.classList.contains('open')) return;
		if (!links.contains(e.target) && !toggle.contains(e.target)) setNavOpen(false);
	});

	// Widening past the breakpoint restores the horizontal link row, so a menu
	// left open would otherwise keep the hamburger stuck in its "X" state.
	window.addEventListener('resize', () => {
		if (window.innerWidth > 1100 && links.classList.contains('open')) setNavOpen(false);
	});
}

// Style-card image carousels (fade + dots, auto-rotate)
document.querySelectorAll('[data-carousel]').forEach(root => {
	const slides = root.querySelectorAll('.carousel-slide');
	const dots = root.querySelectorAll('.carousel-dot');
	let idx = 0;
	let timer;

	function show(i) {
		idx = (i + slides.length) % slides.length;
		slides.forEach((s, n) => s.classList.toggle('active', n === idx));
		dots.forEach((d, n) => d.classList.toggle('active', n === idx));
	}
	function resetTimer() {
		clearInterval(timer);
		if (slides.length > 1) timer = setInterval(() => show(idx + 1), 4500);
	}

	root.querySelector('.carousel-prev')?.addEventListener('click', () => { show(idx - 1); resetTimer(); });
	root.querySelector('.carousel-next')?.addEventListener('click', () => { show(idx + 1); resetTimer(); });
	dots.forEach((d, n) => d.addEventListener('click', () => { show(n); resetTimer(); }));

	resetTimer();
});

// Gallery carousel (sliding track) — only present on the homepage
const galleryTrack = document.getElementById('galleryTrack');
const gallerySlides = document.querySelectorAll('.gallery-slide');
const galleryDotsWrap = document.getElementById('galleryDots');
const galleryPrevBtn = document.getElementById('galleryPrev');
const galleryNextBtn = document.getElementById('galleryNext');

if (galleryTrack && galleryDotsWrap && galleryPrevBtn && galleryNextBtn) {
	gallerySlides.forEach((_, n) => {
		const dot = document.createElement('span');
		dot.className = 'carousel-dot' + (n === 0 ? ' active' : '');
		dot.addEventListener('click', () => goToGallerySlide(n));
		galleryDotsWrap.appendChild(dot);
	});
	const galleryDots = galleryDotsWrap.querySelectorAll('.carousel-dot');

	let galleryIndex = 0;
	const galleryTotal = gallerySlides.length;

	function goToGallerySlide(i) {
		galleryIndex = (i + galleryTotal) % galleryTotal;
		galleryTrack.style.transform = `translateX(-${galleryIndex * 100}%)`;
		galleryDots.forEach((d, n) => d.classList.toggle('active', n === galleryIndex));
	}
	galleryPrevBtn.addEventListener('click', () => goToGallerySlide(galleryIndex - 1));
	galleryNextBtn.addEventListener('click', () => goToGallerySlide(galleryIndex + 1));
}

// Gallery lightbox — only present on the homepage
const lightbox = document.getElementById('lightbox');
const backdrop = document.getElementById('lightboxBackdrop');
const lightboxImg = document.getElementById('lightboxImg');
const closeBtn = document.getElementById('lightboxClose');
const prevBtn = document.getElementById('lightboxPrev');
const nextBtn = document.getElementById('lightboxNext');

if (lightbox && backdrop && lightboxImg && closeBtn && prevBtn && nextBtn) {
	let current = 0;
	let lastFocused = null;
	const total = gallerySlides.length;

	function openLightbox(index) {
		lastFocused = document.activeElement;
		current = index;
		updateLightbox();
		lightbox.classList.add('open');
		backdrop.classList.add('open');
		lightbox.setAttribute('aria-hidden', 'false');
		document.body.style.overflow = 'hidden';
		closeBtn.focus();
	}

	function closeLightbox() {
		if (lightbox.contains(document.activeElement)) {
			document.activeElement.blur();
		}
		lightbox.classList.remove('open');
		backdrop.classList.remove('open');
		lightbox.setAttribute('aria-hidden', 'true');
		document.body.style.overflow = '';
		lastFocused?.focus();
	}

	function updateLightbox() {
		const item = gallerySlides[current];
		const img = item.querySelector('img');
		lightboxImg.src = img.src;
		lightboxImg.alt = img.alt;
	}

	gallerySlides.forEach((item, i) => {
		item.addEventListener('click', () => openLightbox(i));
	});
	closeBtn.addEventListener('click', closeLightbox);
	backdrop.addEventListener('click', closeLightbox);
	prevBtn.addEventListener('click', () => { current = (current - 1 + total) % total; updateLightbox(); });
	nextBtn.addEventListener('click', () => { current = (current + 1) % total; updateLightbox(); });
	document.addEventListener('keydown', e => {
		if (!lightbox.classList.contains('open')) return;
		if (e.key === 'Escape') closeLightbox();
		if (e.key === 'ArrowLeft') { current = (current - 1 + total) % total; updateLightbox(); }
		if (e.key === 'ArrowRight') { current = (current + 1) % total; updateLightbox(); }
	});
}

// Interest modal (per-shed inquiry form on /instock) — only present there
const interestModal = document.getElementById('interestModal');
if (interestModal) {
	const interestBackdrop = document.getElementById('interestModalBackdrop');
	const interestCloseBtn = document.getElementById('interestModalClose');
	const interestItemId = document.getElementById('interestItemId');
	const interestSummary = document.getElementById('interestSummary');
	let interestLastFocused = null;

	function openInterestModal(itemId, summary) {
		interestLastFocused = document.activeElement;
		interestItemId.value = itemId;
		interestSummary.textContent = summary;
		interestModal.classList.add('open');
		interestBackdrop.classList.add('open');
		interestModal.setAttribute('aria-hidden', 'false');
		document.body.style.overflow = 'hidden';
	}

	function closeInterestModal() {
		if (interestModal.contains(document.activeElement)) {
			document.activeElement.blur();
		}
		interestModal.classList.remove('open');
		interestBackdrop.classList.remove('open');
		interestModal.setAttribute('aria-hidden', 'true');
		document.body.style.overflow = '';
		interestLastFocused?.focus();
	}

	document.querySelectorAll('.interest-btn').forEach(btn => {
		btn.addEventListener('click', () => {
			openInterestModal(btn.dataset.itemId, btn.dataset.itemSummary);
		});
	});
	interestCloseBtn.addEventListener('click', closeInterestModal);
	interestBackdrop.addEventListener('click', closeInterestModal);
	document.addEventListener('keydown', e => {
		if (interestModal.classList.contains('open') && e.key === 'Escape') closeInterestModal();
	});
}

// Pricing tab switcher
document.querySelectorAll('.pricing-tab').forEach(tab => {
	tab.addEventListener('click', () => {
		document.querySelectorAll('.pricing-tab').forEach(t => t.classList.remove('active'));
		document.querySelectorAll('.pricing-pane').forEach(p => p.classList.remove('active'));
		tab.classList.add('active');
		document.getElementById('tab-' + tab.dataset.tab).classList.add('active');
	});
});

// Double-submit guard for plain (non-htmx) forms — the admin create/edit
// screens. Those are multipart POSTs that can take seconds while photos
// upload, which is exactly the window in which an impatient second click
// creates a duplicate inventory item.
//
// The htmx-driven public forms don't need this: they use hx-disabled-elt to
// disable the button for the life of the request and hx-sync="this:drop" to
// discard a duplicate request fired before the first finishes.
//
// Guarded on the submit event rather than click, so it only fires once the
// browser's own HTML5 validation (required, type=email, min) has passed —
// disabling on click would lock the button on an invalid form.
document.querySelectorAll('form.admin-form').forEach(form => {
	form.addEventListener('submit', () => {
		const btn = form.querySelector('button[type="submit"]');
		if (!btn) return;
		// Defer past this event so the button's value still posts with the form.
		setTimeout(() => {
			btn.disabled = true;
			btn.dataset.originalText = btn.textContent;
			btn.textContent = 'Saving...';
		}, 0);
	});
});
