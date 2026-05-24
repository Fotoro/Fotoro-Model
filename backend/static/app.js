const API = {
    async post(endpoint, body) {
      const res = await fetch(endpoint, { method: 'POST', body });
      if (!res.ok) throw new Error(await res.text());
      return res.json();
    },
    async get(endpoint) {
      const res = await fetch(endpoint);
      if (!res.ok) throw new Error(await res.text());
      return res.json();
    }
  };
  
  const app = {
    galleryOffset: 0,
    galleryLoading: false,
    hasMore: true,
    currentQuery: '',
    currentImageId: null,
  
    init() {
      this.bindNav();
      this.bindSearch();
      this.bindModal();
      this.bindIngest();
      this.bindInfiniteScroll();
  
      document.addEventListener('keydown', e => {
        if (e.key === 'Escape') this.closeModal();
        if (e.key === '/' && document.activeElement.tagName !== 'INPUT') {
          e.preventDefault();
          this.showView('search');
          document.getElementById('search-input').focus();
        }
      });
  
      // Default to Gallery so user sees photos by date immediately
      this.showView('gallery');
      this.loadGallery(true);
      this.loadStats();
    },
  
    bindNav() {
      document.querySelectorAll('.nav-item').forEach(btn => {
        btn.addEventListener('click', () => {
          document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
          btn.classList.add('active');
          this.showView(btn.dataset.view);
          if (btn.dataset.view === 'gallery') this.loadGallery(true);
          if (btn.dataset.view === 'persons') this.loadPersons();
          if (btn.dataset.view === 'stats') this.loadStats();
        });
      });
    },
  
    showView(name) {
      document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
      document.getElementById(`view-${name}`).classList.add('active');
    },
  
    bindInfiniteScroll() {
      const sentinel = document.getElementById('gallery-sentinel');
      const observer = new IntersectionObserver(entries => {
        if (entries[0].isIntersecting && this.hasMore && !this.galleryLoading) {
          this.loadGallery();
        }
      }, { root: document.querySelector('.main'), threshold: 0 });
      observer.observe(sentinel);
    },
  
    formatDate(d) {
      if (!d || d === 'Unknown') return 'Unknown Date';
      const now = new Date();
      const today = `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}`;
      if (d === today) return 'Today';
      const yesterday = new Date(now); yesterday.setDate(yesterday.getDate()-1);
      const yd = `${yesterday.getFullYear()}-${String(yesterday.getMonth()+1).padStart(2,'0')}-${String(yesterday.getDate()).padStart(2,'0')}`;
      if (d === yd) return 'Yesterday';
      const dt = new Date(d);
      if (!isNaN(dt)) return dt.toLocaleDateString(undefined, { weekday: 'long', year: 'numeric', month: 'short', day: 'numeric' });
      return d;
    },
  
    photoCard(img) {
      return `
        <div class="photo-card" onclick="app.openModal(${img.id})">
          <img src="/api/image/${img.id}?thumb=1" loading="lazy" alt="${img.filename}" onerror="this.style.opacity=0.3">
          <div class="photo-info">
            <div class="photo-title">${img.filename}</div>
            <div class="photo-meta">${img.caption || ''}</div>
          </div>
        </div>`;
    },
  
    async loadGallery(reset = false) {
      if (reset) {
        this.galleryOffset = 0;
        this.hasMore = true;
        document.getElementById('gallery-grid').innerHTML = '';
      }
      if (this.galleryLoading || !this.hasMore) return;
      this.galleryLoading = true;
      this.setStatus('Loading photos…');
  
      try {
        const data = await API.get(`/api/gallery?offset=${this.galleryOffset}&limit=50`);
        const container = document.getElementById('gallery-grid');
        const groups = data.groups || {};
  
        const groupKeys = Object.keys(groups);
        if (groupKeys.length === 0) {
          this.hasMore = false;
          document.getElementById('gallery-sentinel').textContent = 'No more photos';
          this.setStatus('All photos loaded');
          return;
        }
  
        groupKeys.forEach(date => {
          let section = document.getElementById(`date-${date}`);
          if (!section) {
            section = document.createElement('div');
            section.className = 'date-section';
            section.id = `date-${date}`;
            section.innerHTML = `<div class="date-header">${this.formatDate(date)}</div><div class="date-photos"></div>`;
            container.appendChild(section);
          }
          const grid = section.querySelector('.date-photos');
          groups[date].forEach(img => grid.insertAdjacentHTML('beforeend', this.photoCard(img)));
        });
  
        this.galleryOffset += 50;
        this.hasMore = groupKeys.length > 0; // assume more until server returns empty
        this.setStatus('Ready');
      } catch (e) {
        this.setStatus('Failed to load gallery');
        console.error(e);
      } finally {
        this.galleryLoading = false;
      }
    },
  
    bindSearch() {
      const input = document.getElementById('search-input');
      const btn = document.getElementById('search-btn');
      const run = () => {
        const q = input.value.trim();
        if (!q) return;
        this.currentQuery = q;
        this.doSearch(q);
      };
      btn.addEventListener('click', run);
      input.addEventListener('keydown', e => { if (e.key === 'Enter') run(); });
    },
  
    async doSearch(q) {
      this.setStatus('Searching…');
      try {
        const data = await API.post('/api/search', new URLSearchParams({ q, k: 30 }));
        const grid = document.getElementById('search-results');
        if (!data.results.length) {
          grid.innerHTML = '<div style="color:var(--text-dim);padding:40px 0;">No results. Try another description.</div>';
          this.setStatus('0 results');
          return;
        }
        grid.innerHTML = data.results.map(r => this.photoCard(r)).join('');
        this.setStatus(`${data.count} results`);
      } catch (e) {
        this.setStatus('Search failed');
        console.error(e);
      }
    },
  
    async loadPersons() {
      this.setStatus('Loading people…');
      try {
        const data = await API.get('/api/persons');
        const grid = document.getElementById('persons-grid');
        if (!data.length) {
          grid.innerHTML = '<div style="color:var(--text-dim);padding:20px;">No people detected yet. Run import with face detection enabled.</div>';
          return;
        }
        grid.innerHTML = data.map(p => `
          <div class="person-card" onclick="app.showPersonPhotos(${p.id})">
            <img src="/api/face/${p.id}" loading="lazy" onerror="this.style.display='none'">
            <div class="name">${p.name}</div>
            <div class="count">${p.photo_count} photos</div>
          </div>
        `).join('');
        this.setStatus(`${data.length} people`);
      } catch (e) {
        this.setStatus('Failed to load people');
      }
    },
  
    async showPersonPhotos(personId) {
      this.showView('gallery');
      document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
      this.setStatus('Loading photos…');
      try {
        const data = await API.get(`/api/persons/${personId}/photos`);
        const container = document.getElementById('gallery-grid');
        container.innerHTML = '';
        document.getElementById('gallery-sentinel').style.display = 'none';
        if (!data.photos.length) {
          container.innerHTML = '<div style="color:var(--text-dim);padding:20px;">No photos for this person.</div>';
          return;
        }
        const section = document.createElement('div');
        section.className = 'date-section';
        section.innerHTML = `<div class="date-header">${data.photos[0].filename || 'Photos'}</div><div class="date-photos"></div>`;
        container.appendChild(section);
        const grid = section.querySelector('.date-photos');
        data.photos.forEach(p => grid.insertAdjacentHTML('beforeend', this.photoCard(p)));
        this.setStatus(`${data.count} photos`);
      } catch (e) {
        this.setStatus('Failed');
      }
    },
  
    async loadStats() {
      try {
        const s = await API.get('/api/stats');
        const grid = document.getElementById('stats-grid');
        const stats = [
          { label: 'Photos', value: s.images },
          { label: 'People', value: s.persons },
          { label: 'Face Appearances', value: s.face_appearances },
          { label: 'Visual Vectors', value: s.visual_vectors },
          { label: 'Text Vectors', value: s.text_vectors },
          { label: 'Face Vectors', value: s.face_vectors },
        ];
        grid.innerHTML = stats.map(st => `
          <div class="stat-card">
            <div class="stat-value">${st.value.toLocaleString()}</div>
            <div class="stat-label">${st.label}</div>
          </div>
        `).join('');
      } catch (e) { console.error(e); }
    },
  
    bindIngest() {
      const btn = document.getElementById('ingest-btn');
      btn.addEventListener('click', async () => {
        const path = document.getElementById('ingest-path').value.trim();
        if (!path) return alert('Enter a folder path');
        const recursive = document.getElementById('ingest-recursive').checked;
        const noFaces = document.getElementById('ingest-no-faces').checked;
        btn.disabled = true;
        document.getElementById('ingest-progress').classList.remove('hidden');
        try {
          const res = await API.post('/api/ingest', new URLSearchParams({ folder: path, recursive, no_faces: noFaces }));
          this.pollJob(res.job_id);
        } catch (e) {
          alert('Import failed: ' + e.message);
          btn.disabled = false;
        }
      });
    },
  
    async pollJob(jobId) {
      const fill = document.getElementById('progress-fill');
      const text = document.getElementById('progress-text');
      const btn = document.getElementById('ingest-btn');
      const iv = setInterval(async () => {
        try {
          const st = await API.get(`/api/ingest/${jobId}`);
          fill.style.width = st.progress + '%';
          text.textContent = `${st.status}: ${st.message || ''}`;
          if (st.status === 'completed' || st.status === 'failed') {
            clearInterval(iv);
            btn.disabled = false;
            if (st.status === 'completed') {
              text.textContent = 'Import complete!';
              this.loadGallery(true);
              this.loadStats();
            }
          }
        } catch (e) { clearInterval(iv); btn.disabled = false; }
      }, 2000);
    },
  
    async openModal(id) {
      this.currentImageId = id;
      const modal = document.getElementById('modal');
      const img = document.getElementById('modal-img');
      img.src = `/api/image/${id}`;
      modal.classList.add('active');
      try {
        const meta = await API.get(`/api/images/${id}`);
        document.getElementById('modal-filename').textContent = meta.filename;
        document.getElementById('modal-caption').textContent = meta.caption || 'No caption';
        const tags = [...(meta.tags || []).map(t => t.tag || t), ...(meta.colors || [])];
        document.getElementById('modal-tags').innerHTML = tags.map(t => `<span class="tag">${t}</span>`).join('');
        const details = [];
        if (meta.date) details.push(`Date: ${meta.date}`);
        if (meta.width && meta.height) details.push(`${meta.width}×${meta.height}`);
        if (meta.face_count) details.push(`${meta.face_count} faces`);
        if (meta.ocr) details.push(`OCR: ${meta.ocr.slice(0, 120)}`);
        document.getElementById('modal-details').innerHTML = details.join(' • ');
      } catch (e) {
        document.getElementById('modal-filename').textContent = 'Image ' + id;
      }
    },
  
    closeModal() {
      document.getElementById('modal').classList.remove('active');
      document.getElementById('modal-img').src = '';
    },
  
    bindModal() {
      document.querySelector('.modal-close').addEventListener('click', () => this.closeModal());
      document.querySelector('.modal-backdrop').addEventListener('click', () => this.closeModal());
      document.getElementById('modal-feedback-good').addEventListener('click', () => this.sendFeedback(true));
      document.getElementById('modal-feedback-bad').addEventListener('click', () => this.sendFeedback(false));
    },
  
    async sendFeedback(relevant) {
      if (!this.currentImageId || !this.currentQuery) return;
      try {
        await API.post('/api/feedback', new URLSearchParams({
          image_id: this.currentImageId, query: this.currentQuery, relevant, note: ''
        }));
        this.setStatus('Feedback saved');
      } catch (e) { this.setStatus('Feedback failed'); }
    },
  
    setStatus(msg) {
      document.getElementById('status').textContent = msg;
    }
  };
  
  app.init();