package localize

import "strings"

var options = []Option{
	{English, "English"}, {"ar", "العربية"}, {"bn", "বাংলা"}, {"ca", "Català"},
	{"cs", "Čeština"}, {"da", "Dansk"}, {"de", "Deutsch"}, {"el", "Ελληνικά"},
	{"es", "Español"}, {"fi", "Suomi"}, {"fr", "Français"}, {"he", "עברית"},
	{"hi", "हिन्दी"}, {"hr", "Hrvatski"}, {"hu", "Magyar"}, {"id", "Bahasa Indonesia"},
	{"it", "Italiano"}, {"ja", "日本語"}, {"ko", "한국어"}, {"nl", "Nederlands"},
	{"no", "Norsk"}, {"pl", "Polski"}, {"pt-BR", "Português (Brasil)"}, {"ro", "Română"},
	{"ru", "Русский"}, {"sk", "Slovenčina"}, {"sv", "Svenska"}, {"th", "ไทย"},
	{"tr", "Türkçe"}, {"uk", "Українська"}, {"vi", "Tiếng Việt"},
	{"zh-CN", "简体中文"}, {"zh-TW", "繁體中文"},
}

var catalog = map[Locale]map[string]string{
	English: englishCatalog,
	"ar":    words("نظرة عامة", "المواقع", "النشاط", "المراقبة", "قواعد البيانات", "التخزين", "مزودو DNS", "المستخدمون", "حسابي", "أدوات الموقع", "إعدادات الخادم", "الحساب والأمان", "تسجيل الخروج", "النسخ الاحتياطية", "الملفات", "بيئة الاختبار", "SSL والأمان", "السجلات والاستخدام", "الإعدادات", "إنشاء موقع", "المواقع الإجمالية", "موارد الخادم", "فتح المراقبة", "عرض كل المواقع"),
	"bn":    words("সংক্ষিপ্ত বিবরণ", "সাইটসমূহ", "কার্যকলাপ", "পর্যবেক্ষণ", "ডেটাবেস", "স্টোরেজ", "DNS প্রদানকারী", "ব্যবহারকারী", "আমার অ্যাকাউন্ট", "সাইটের সরঞ্জাম", "সার্ভার সেটিংস", "অ্যাকাউন্ট ও নিরাপত্তা", "সাইন আউট", "ব্যাকআপ", "ফাইল", "স্টেজিং", "SSL ও নিরাপত্তা", "লগ ও ব্যবহার", "সেটিংস", "সাইট তৈরি করুন", "মোট সাইট", "সার্ভার রিসোর্স", "পর্যবেক্ষণ খুলুন", "সব সাইট দেখুন"),
	"ca":    words("Resum", "Llocs", "Activitat", "Monitoratge", "Bases de dades", "Emmagatzematge", "Proveïdors DNS", "Usuaris", "El meu compte", "Eines del lloc", "Configuració del servidor", "Compte i seguretat", "Tanca la sessió", "Còpies de seguretat", "Fitxers", "Preproducció", "SSL i seguretat", "Registres i ús", "Configuració", "Crea un lloc", "Total de llocs", "Recursos del servidor", "Obre el monitoratge", "Mostra tots els llocs"),
	"cs":    words("Přehled", "Weby", "Aktivita", "Monitorování", "Databáze", "Úložiště", "Poskytovatelé DNS", "Uživatelé", "Můj účet", "Nástroje webu", "Nastavení serveru", "Účet a zabezpečení", "Odhlásit se", "Zálohy", "Soubory", "Testovací prostředí", "SSL a zabezpečení", "Protokoly a využití", "Nastavení", "Vytvořit web", "Celkem webů", "Prostředky serveru", "Otevřít monitorování", "Zobrazit všechny weby"),
	"da":    words("Oversigt", "Websteder", "Aktivitet", "Overvågning", "Databaser", "Lager", "DNS-udbydere", "Brugere", "Min konto", "Webstedsværktøjer", "Serverindstillinger", "Konto og sikkerhed", "Log ud", "Sikkerhedskopier", "Filer", "Testmiljø", "SSL og sikkerhed", "Logfiler og forbrug", "Indstillinger", "Opret websted", "Websteder i alt", "Serverressourcer", "Åbn overvågning", "Vis alle websteder"),
	"de":    words("Übersicht", "Websites", "Aktivität", "Überwachung", "Datenbanken", "Speicher", "DNS-Anbieter", "Benutzer", "Mein Konto", "Website-Werkzeuge", "Servereinstellungen", "Konto und Sicherheit", "Abmelden", "Sicherungen", "Dateien", "Testumgebung", "SSL und Sicherheit", "Protokolle und Nutzung", "Einstellungen", "Website erstellen", "Websites insgesamt", "Serverressourcen", "Überwachung öffnen", "Alle Websites anzeigen"),
	"el":    words("Επισκόπηση", "Ιστότοποι", "Δραστηριότητα", "Παρακολούθηση", "Βάσεις δεδομένων", "Αποθήκευση", "Πάροχοι DNS", "Χρήστες", "Ο λογαριασμός μου", "Εργαλεία ιστοτόπου", "Ρυθμίσεις διακομιστή", "Λογαριασμός και ασφάλεια", "Αποσύνδεση", "Αντίγραφα ασφαλείας", "Αρχεία", "Δοκιμαστικό περιβάλλον", "SSL και ασφάλεια", "Αρχεία καταγραφής και χρήση", "Ρυθμίσεις", "Δημιουργία ιστοτόπου", "Σύνολο ιστοτόπων", "Πόροι διακομιστή", "Άνοιγμα παρακολούθησης", "Προβολή όλων των ιστοτόπων"),
	"es":    words("Resumen", "Sitios", "Actividad", "Monitorización", "Bases de datos", "Almacenamiento", "Proveedores DNS", "Usuarios", "Mi cuenta", "Herramientas del sitio", "Ajustes del servidor", "Cuenta y seguridad", "Cerrar sesión", "Copias de seguridad", "Archivos", "Entorno de pruebas", "SSL y seguridad", "Registros y uso", "Ajustes", "Crear sitio", "Sitios totales", "Recursos del servidor", "Abrir monitorización", "Ver todos los sitios"),
	"fi":    words("Yleiskatsaus", "Sivustot", "Toiminta", "Valvonta", "Tietokannat", "Tallennustila", "DNS-palveluntarjoajat", "Käyttäjät", "Oma tili", "Sivuston työkalut", "Palvelimen asetukset", "Tili ja turvallisuus", "Kirjaudu ulos", "Varmuuskopiot", "Tiedostot", "Testiympäristö", "SSL ja turvallisuus", "Lokit ja käyttö", "Asetukset", "Luo sivusto", "Sivustoja yhteensä", "Palvelinresurssit", "Avaa valvonta", "Näytä kaikki sivustot"),
	"fr":    words("Vue d’ensemble", "Sites", "Activité", "Surveillance", "Bases de données", "Stockage", "Fournisseurs DNS", "Utilisateurs", "Mon compte", "Outils du site", "Paramètres du serveur", "Compte et sécurité", "Se déconnecter", "Sauvegardes", "Fichiers", "Préproduction", "SSL et sécurité", "Journaux et utilisation", "Paramètres", "Créer un site", "Nombre de sites", "Ressources du serveur", "Ouvrir la surveillance", "Voir tous les sites"),
	"he":    words("סקירה", "אתרים", "פעילות", "ניטור", "מסדי נתונים", "אחסון", "ספקי DNS", "משתמשים", "החשבון שלי", "כלי האתר", "הגדרות שרת", "חשבון ואבטחה", "יציאה", "גיבויים", "קבצים", "סביבת בדיקות", "SSL ואבטחה", "יומנים ושימוש", "הגדרות", "יצירת אתר", "סך האתרים", "משאבי שרת", "פתיחת ניטור", "הצגת כל האתרים"),
	"hi":    words("अवलोकन", "साइटें", "गतिविधि", "निगरानी", "डेटाबेस", "स्टोरेज", "DNS प्रदाता", "उपयोगकर्ता", "मेरा खाता", "साइट उपकरण", "सर्वर सेटिंग", "खाता और सुरक्षा", "साइन आउट", "बैकअप", "फ़ाइलें", "स्टेजिंग", "SSL और सुरक्षा", "लॉग और उपयोग", "सेटिंग", "साइट बनाएँ", "कुल साइटें", "सर्वर संसाधन", "निगरानी खोलें", "सभी साइटें देखें"),
	"hr":    words("Pregled", "Web-mjesta", "Aktivnost", "Nadzor", "Baze podataka", "Pohrana", "DNS pružatelji", "Korisnici", "Moj račun", "Alati web-mjesta", "Postavke poslužitelja", "Račun i sigurnost", "Odjava", "Sigurnosne kopije", "Datoteke", "Testno okruženje", "SSL i sigurnost", "Zapisi i upotreba", "Postavke", "Izradi web-mjesto", "Ukupno web-mjesta", "Resursi poslužitelja", "Otvori nadzor", "Prikaži sva web-mjesta"),
	"hu":    words("Áttekintés", "Webhelyek", "Tevékenység", "Felügyelet", "Adatbázisok", "Tárhely", "DNS-szolgáltatók", "Felhasználók", "Saját fiók", "Webhelyeszközök", "Kiszolgáló beállításai", "Fiók és biztonság", "Kijelentkezés", "Biztonsági mentések", "Fájlok", "Tesztkörnyezet", "SSL és biztonság", "Naplók és használat", "Beállítások", "Webhely létrehozása", "Webhelyek összesen", "Kiszolgáló-erőforrások", "Felügyelet megnyitása", "Összes webhely megtekintése"),
	"id":    words("Ringkasan", "Situs", "Aktivitas", "Pemantauan", "Basis data", "Penyimpanan", "Penyedia DNS", "Pengguna", "Akun saya", "Alat situs", "Pengaturan server", "Akun dan keamanan", "Keluar", "Cadangan", "File", "Lingkungan uji", "SSL dan keamanan", "Log dan penggunaan", "Pengaturan", "Buat situs", "Total situs", "Sumber daya server", "Buka pemantauan", "Lihat semua situs"),
	"it":    words("Panoramica", "Siti", "Attività", "Monitoraggio", "Database", "Archiviazione", "Provider DNS", "Utenti", "Il mio account", "Strumenti del sito", "Impostazioni server", "Account e sicurezza", "Esci", "Backup", "File", "Ambiente di test", "SSL e sicurezza", "Log e utilizzo", "Impostazioni", "Crea sito", "Siti totali", "Risorse del server", "Apri monitoraggio", "Visualizza tutti i siti"),
	"ja":    words("概要", "サイト", "アクティビティ", "監視", "データベース", "ストレージ", "DNSプロバイダー", "ユーザー", "マイアカウント", "サイトツール", "サーバー設定", "アカウントとセキュリティ", "ログアウト", "バックアップ", "ファイル", "ステージング", "SSLとセキュリティ", "ログと使用状況", "設定", "サイトを作成", "サイト総数", "サーバーリソース", "監視を開く", "すべてのサイトを表示"),
	"ko":    words("개요", "사이트", "활동", "모니터링", "데이터베이스", "저장소", "DNS 제공업체", "사용자", "내 계정", "사이트 도구", "서버 설정", "계정 및 보안", "로그아웃", "백업", "파일", "스테이징", "SSL 및 보안", "로그 및 사용량", "설정", "사이트 만들기", "전체 사이트", "서버 리소스", "모니터링 열기", "모든 사이트 보기"),
	"nl":    words("Overzicht", "Sites", "Activiteit", "Bewaking", "Databases", "Opslag", "DNS-providers", "Gebruikers", "Mijn account", "Sitehulpmiddelen", "Serverinstellingen", "Account en beveiliging", "Afmelden", "Back-ups", "Bestanden", "Testomgeving", "SSL en beveiliging", "Logboeken en gebruik", "Instellingen", "Site maken", "Totaal aantal sites", "Serverbronnen", "Bewaking openen", "Alle sites bekijken"),
	"no":    words("Oversikt", "Nettsteder", "Aktivitet", "Overvåking", "Databaser", "Lagring", "DNS-leverandører", "Brukere", "Min konto", "Nettstedverktøy", "Serverinnstillinger", "Konto og sikkerhet", "Logg ut", "Sikkerhetskopier", "Filer", "Testmiljø", "SSL og sikkerhet", "Logger og bruk", "Innstillinger", "Opprett nettsted", "Nettsteder totalt", "Serverressurser", "Åpne overvåking", "Vis alle nettsteder"),
	"pl":    words("Przegląd", "Witryny", "Aktywność", "Monitorowanie", "Bazy danych", "Pamięć", "Dostawcy DNS", "Użytkownicy", "Moje konto", "Narzędzia witryny", "Ustawienia serwera", "Konto i bezpieczeństwo", "Wyloguj się", "Kopie zapasowe", "Pliki", "Środowisko testowe", "SSL i bezpieczeństwo", "Dzienniki i użycie", "Ustawienia", "Utwórz witrynę", "Łączna liczba witryn", "Zasoby serwera", "Otwórz monitorowanie", "Wyświetl wszystkie witryny"),
	"pt-BR": words("Visão geral", "Sites", "Atividade", "Monitoramento", "Bancos de dados", "Armazenamento", "Provedores de DNS", "Usuários", "Minha conta", "Ferramentas do site", "Configurações do servidor", "Conta e segurança", "Sair", "Backups", "Arquivos", "Ambiente de teste", "SSL e segurança", "Logs e uso", "Configurações", "Criar site", "Total de sites", "Recursos do servidor", "Abrir monitoramento", "Ver todos os sites"),
	"ro":    words("Prezentare generală", "Site-uri", "Activitate", "Monitorizare", "Baze de date", "Stocare", "Furnizori DNS", "Utilizatori", "Contul meu", "Instrumente site", "Setări server", "Cont și securitate", "Deconectare", "Copii de siguranță", "Fișiere", "Mediu de testare", "SSL și securitate", "Jurnale și utilizare", "Setări", "Creează site", "Total site-uri", "Resurse server", "Deschide monitorizarea", "Vezi toate site-urile"),
	"ru":    words("Обзор", "Сайты", "Действия", "Мониторинг", "Базы данных", "Хранилище", "Провайдеры DNS", "Пользователи", "Моя учётная запись", "Инструменты сайта", "Настройки сервера", "Учётная запись и безопасность", "Выйти", "Резервные копии", "Файлы", "Тестовая среда", "SSL и безопасность", "Журналы и использование", "Настройки", "Создать сайт", "Всего сайтов", "Ресурсы сервера", "Открыть мониторинг", "Показать все сайты"),
	"sk":    words("Prehľad", "Weby", "Aktivita", "Monitorovanie", "Databázy", "Úložisko", "Poskytovatelia DNS", "Používatelia", "Môj účet", "Nástroje webu", "Nastavenia servera", "Účet a zabezpečenie", "Odhlásiť sa", "Zálohy", "Súbory", "Testovacie prostredie", "SSL a zabezpečenie", "Denníky a využitie", "Nastavenia", "Vytvoriť web", "Weby spolu", "Prostriedky servera", "Otvoriť monitorovanie", "Zobraziť všetky weby"),
	"sv":    words("Översikt", "Webbplatser", "Aktivitet", "Övervakning", "Databaser", "Lagring", "DNS-leverantörer", "Användare", "Mitt konto", "Webbplatsverktyg", "Serverinställningar", "Konto och säkerhet", "Logga ut", "Säkerhetskopior", "Filer", "Testmiljö", "SSL och säkerhet", "Loggar och användning", "Inställningar", "Skapa webbplats", "Totalt antal webbplatser", "Serverresurser", "Öppna övervakning", "Visa alla webbplatser"),
	"th":    words("ภาพรวม", "เว็บไซต์", "กิจกรรม", "การตรวจสอบ", "ฐานข้อมูล", "พื้นที่จัดเก็บ", "ผู้ให้บริการ DNS", "ผู้ใช้", "บัญชีของฉัน", "เครื่องมือเว็บไซต์", "การตั้งค่าเซิร์ฟเวอร์", "บัญชีและความปลอดภัย", "ออกจากระบบ", "ข้อมูลสำรอง", "ไฟล์", "สภาพแวดล้อมทดสอบ", "SSL และความปลอดภัย", "บันทึกและการใช้งาน", "การตั้งค่า", "สร้างเว็บไซต์", "เว็บไซต์ทั้งหมด", "ทรัพยากรเซิร์ฟเวอร์", "เปิดการตรวจสอบ", "ดูเว็บไซต์ทั้งหมด"),
	"tr":    words("Genel bakış", "Siteler", "Etkinlik", "İzleme", "Veritabanları", "Depolama", "DNS sağlayıcıları", "Kullanıcılar", "Hesabım", "Site araçları", "Sunucu ayarları", "Hesap ve güvenlik", "Çıkış yap", "Yedekler", "Dosyalar", "Test ortamı", "SSL ve güvenlik", "Günlükler ve kullanım", "Ayarlar", "Site oluştur", "Toplam site", "Sunucu kaynakları", "İzlemeyi aç", "Tüm siteleri görüntüle"),
	"uk":    words("Огляд", "Сайти", "Активність", "Моніторинг", "Бази даних", "Сховище", "Постачальники DNS", "Користувачі", "Мій обліковий запис", "Інструменти сайту", "Налаштування сервера", "Обліковий запис і безпека", "Вийти", "Резервні копії", "Файли", "Тестове середовище", "SSL і безпека", "Журнали та використання", "Налаштування", "Створити сайт", "Усього сайтів", "Ресурси сервера", "Відкрити моніторинг", "Переглянути всі сайти"),
	"vi":    words("Tổng quan", "Trang web", "Hoạt động", "Giám sát", "Cơ sở dữ liệu", "Lưu trữ", "Nhà cung cấp DNS", "Người dùng", "Tài khoản của tôi", "Công cụ trang web", "Cài đặt máy chủ", "Tài khoản và bảo mật", "Đăng xuất", "Bản sao lưu", "Tệp", "Môi trường thử nghiệm", "SSL và bảo mật", "Nhật ký và mức sử dụng", "Cài đặt", "Tạo trang web", "Tổng số trang web", "Tài nguyên máy chủ", "Mở giám sát", "Xem tất cả trang web"),
	"zh-CN": words("概览", "站点", "活动", "监控", "数据库", "存储", "DNS 提供商", "用户", "我的账户", "站点工具", "服务器设置", "账户与安全", "退出登录", "备份", "文件", "预发布环境", "SSL 与安全", "日志与用量", "设置", "创建站点", "站点总数", "服务器资源", "打开监控", "查看所有站点"),
	"zh-TW": words("總覽", "網站", "活動", "監控", "資料庫", "儲存空間", "DNS 供應商", "使用者", "我的帳戶", "網站工具", "伺服器設定", "帳戶與安全性", "登出", "備份", "檔案", "預備環境", "SSL 與安全性", "記錄與用量", "設定", "建立網站", "網站總數", "伺服器資源", "開啟監控", "檢視所有網站"),
}

var keys = []string{"overview", "sites", "activity", "monitoring", "databases", "storage", "dns_providers", "users", "my_account", "site_tools", "server_settings", "account_security", "sign_out", "backups", "files", "staging", "ssl_security", "logs_usage", "settings", "create_site", "total_sites", "server_resources", "open_monitoring", "view_all_sites"}

var englishCatalog = map[string]string{
	"overview": "Overview", "sites": "Sites", "activity": "Activity", "monitoring": "Monitoring",
	"databases": "Databases", "storage": "Storage", "dns_providers": "DNS providers", "users": "Users",
	"my_account": "My account", "site_tools": "Site tools", "server_settings": "Server settings",
	"account_security": "Account & security", "sign_out": "Sign out", "backups": "Backups", "files": "Files",
	"staging": "Staging", "ssl_security": "SSL & security", "logs_usage": "Logs & usage", "settings": "Settings",
	"create_site": "Create site", "total_sites": "Total sites", "server_resources": "Server resources",
	"open_monitoring": "Open monitoring", "view_all_sites": "View all sites",
	"language": "Language", "apply": "Apply",
	"wordpress": "WordPress", "dns": "DNS", "hosting": "Hosting", "alerts": "Alerts", "all_sites": "All sites",
	"site": "Site", "access": "Access", "security": "Security", "cron": "Cron", "runtime": "Runtime", "ftp": "FTP",
	"logs": "Logs", "account": "Account", "skip_to_content": "Skip to content", "server": "Server",
}

var selectorWords = map[Locale][2]string{
	"ar": {"اللغة", "تطبيق"}, "bn": {"ভাষা", "প্রয়োগ করুন"}, "ca": {"Idioma", "Aplica"},
	"cs": {"Jazyk", "Použít"}, "da": {"Sprog", "Anvend"}, "de": {"Sprache", "Anwenden"},
	"el": {"Γλώσσα", "Εφαρμογή"}, "es": {"Idioma", "Aplicar"}, "fi": {"Kieli", "Käytä"},
	"fr": {"Langue", "Appliquer"}, "he": {"שפה", "החלה"}, "hi": {"भाषा", "लागू करें"},
	"hr": {"Jezik", "Primijeni"}, "hu": {"Nyelv", "Alkalmaz"}, "id": {"Bahasa", "Terapkan"},
	"it": {"Lingua", "Applica"}, "ja": {"言語", "適用"}, "ko": {"언어", "적용"},
	"nl": {"Taal", "Toepassen"}, "no": {"Språk", "Bruk"}, "pl": {"Język", "Zastosuj"},
	"pt-BR": {"Idioma", "Aplicar"}, "ro": {"Limbă", "Aplică"}, "ru": {"Язык", "Применить"},
	"sk": {"Jazyk", "Použiť"}, "sv": {"Språk", "Använd"}, "th": {"ภาษา", "นำไปใช้"},
	"tr": {"Dil", "Uygula"}, "uk": {"Мова", "Застосувати"}, "vi": {"Ngôn ngữ", "Áp dụng"},
	"zh-CN": {"语言", "应用"}, "zh-TW": {"語言", "套用"},
}

var navigationWords = map[Locale][15]string{
	"ar":    {"ووردبريس", "DNS", "الاستضافة", "التنبيهات", "كل المواقع", "الموقع", "الوصول", "الأمان", "المهام المجدولة", "بيئة التشغيل", "FTP", "السجلات", "الحساب", "تخطي إلى المحتوى", "الخادم"},
	"bn":    {"ওয়ার্ডপ্রেস", "DNS", "হোস্টিং", "সতর্কতা", "সব সাইট", "সাইট", "অ্যাক্সেস", "নিরাপত্তা", "নির্ধারিত কাজ", "রানটাইম", "FTP", "লগ", "অ্যাকাউন্ট", "বিষয়বস্তুতে যান", "সার্ভার"},
	"ca":    {"WordPress", "DNS", "Allotjament", "Alertes", "Tots els llocs", "Lloc", "Accés", "Seguretat", "Tasques programades", "Entorn d’execució", "FTP", "Registres", "Compte", "Ves al contingut", "Servidor"},
	"cs":    {"WordPress", "DNS", "Hosting", "Upozornění", "Všechny weby", "Web", "Přístup", "Zabezpečení", "Plánované úlohy", "Běhové prostředí", "FTP", "Protokoly", "Účet", "Přejít k obsahu", "Server"},
	"da":    {"WordPress", "DNS", "Hosting", "Advarsler", "Alle websteder", "Websted", "Adgang", "Sikkerhed", "Planlagte opgaver", "Kørselsmiljø", "FTP", "Logfiler", "Konto", "Gå til indhold", "Server"},
	"de":    {"WordPress", "DNS", "Hosting", "Warnungen", "Alle Websites", "Website", "Zugriff", "Sicherheit", "Zeitgesteuerte Aufgaben", "Laufzeit", "FTP", "Protokolle", "Konto", "Zum Inhalt springen", "Server"},
	"el":    {"WordPress", "DNS", "Φιλοξενία", "Ειδοποιήσεις", "Όλοι οι ιστότοποι", "Ιστότοπος", "Πρόσβαση", "Ασφάλεια", "Προγραμματισμένες εργασίες", "Περιβάλλον εκτέλεσης", "FTP", "Αρχεία καταγραφής", "Λογαριασμός", "Μετάβαση στο περιεχόμενο", "Διακομιστής"},
	"es":    {"WordPress", "DNS", "Alojamiento", "Alertas", "Todos los sitios", "Sitio", "Acceso", "Seguridad", "Tareas programadas", "Entorno de ejecución", "FTP", "Registros", "Cuenta", "Saltar al contenido", "Servidor"},
	"fi":    {"WordPress", "DNS", "Verkkohotelli", "Hälytykset", "Kaikki sivustot", "Sivusto", "Käyttöoikeus", "Turvallisuus", "Ajastetut tehtävät", "Ajoympäristö", "FTP", "Lokit", "Tili", "Siirry sisältöön", "Palvelin"},
	"fr":    {"WordPress", "DNS", "Hébergement", "Alertes", "Tous les sites", "Site", "Accès", "Sécurité", "Tâches planifiées", "Environnement d’exécution", "FTP", "Journaux", "Compte", "Aller au contenu", "Serveur"},
	"he":    {"וורדפרס", "DNS", "אירוח", "התראות", "כל האתרים", "אתר", "גישה", "אבטחה", "משימות מתוזמנות", "סביבת ריצה", "FTP", "יומנים", "חשבון", "דילוג לתוכן", "שרת"},
	"hi":    {"वर्डप्रेस", "DNS", "होस्टिंग", "अलर्ट", "सभी साइटें", "साइट", "पहुँच", "सुरक्षा", "निर्धारित कार्य", "रनटाइम", "FTP", "लॉग", "खाता", "सामग्री पर जाएँ", "सर्वर"},
	"hr":    {"WordPress", "DNS", "Hosting", "Upozorenja", "Sva web-mjesta", "Web-mjesto", "Pristup", "Sigurnost", "Zakazani zadaci", "Izvršno okruženje", "FTP", "Zapisi", "Račun", "Preskoči na sadržaj", "Poslužitelj"},
	"hu":    {"WordPress", "DNS", "Tárhelyszolgáltatás", "Riasztások", "Összes webhely", "Webhely", "Hozzáférés", "Biztonság", "Ütemezett feladatok", "Futtatókörnyezet", "FTP", "Naplók", "Fiók", "Ugrás a tartalomhoz", "Kiszolgáló"},
	"id":    {"WordPress", "DNS", "Hosting", "Peringatan", "Semua situs", "Situs", "Akses", "Keamanan", "Tugas terjadwal", "Runtime", "FTP", "Log", "Akun", "Lewati ke konten", "Server"},
	"it":    {"WordPress", "DNS", "Hosting", "Avvisi", "Tutti i siti", "Sito", "Accesso", "Sicurezza", "Attività pianificate", "Runtime", "FTP", "Log", "Account", "Vai al contenuto", "Server"},
	"ja":    {"WordPress", "DNS", "ホスティング", "アラート", "すべてのサイト", "サイト", "アクセス", "セキュリティ", "スケジュール済みタスク", "ランタイム", "FTP", "ログ", "アカウント", "コンテンツへ移動", "サーバー"},
	"ko":    {"워드프레스", "DNS", "호스팅", "알림", "모든 사이트", "사이트", "접근", "보안", "예약 작업", "런타임", "FTP", "로그", "계정", "콘텐츠로 건너뛰기", "서버"},
	"nl":    {"WordPress", "DNS", "Hosting", "Meldingen", "Alle sites", "Site", "Toegang", "Beveiliging", "Geplande taken", "Runtime", "FTP", "Logboeken", "Account", "Naar inhoud", "Server"},
	"no":    {"WordPress", "DNS", "Hosting", "Varsler", "Alle nettsteder", "Nettsted", "Tilgang", "Sikkerhet", "Planlagte oppgaver", "Kjøremiljø", "FTP", "Logger", "Konto", "Gå til innhold", "Server"},
	"pl":    {"WordPress", "DNS", "Hosting", "Alerty", "Wszystkie witryny", "Witryna", "Dostęp", "Bezpieczeństwo", "Zaplanowane zadania", "Środowisko uruchomieniowe", "FTP", "Dzienniki", "Konto", "Przejdź do treści", "Serwer"},
	"pt-BR": {"WordPress", "DNS", "Hospedagem", "Alertas", "Todos os sites", "Site", "Acesso", "Segurança", "Tarefas agendadas", "Ambiente de execução", "FTP", "Logs", "Conta", "Pular para o conteúdo", "Servidor"},
	"ro":    {"WordPress", "DNS", "Găzduire", "Alerte", "Toate site-urile", "Site", "Acces", "Securitate", "Sarcini programate", "Mediu de execuție", "FTP", "Jurnale", "Cont", "Salt la conținut", "Server"},
	"ru":    {"WordPress", "DNS", "Хостинг", "Оповещения", "Все сайты", "Сайт", "Доступ", "Безопасность", "Запланированные задачи", "Среда выполнения", "FTP", "Журналы", "Учётная запись", "Перейти к содержимому", "Сервер"},
	"sk":    {"WordPress", "DNS", "Hosting", "Upozornenia", "Všetky weby", "Web", "Prístup", "Zabezpečenie", "Naplánované úlohy", "Behové prostredie", "FTP", "Denníky", "Účet", "Prejsť na obsah", "Server"},
	"sv":    {"WordPress", "DNS", "Webbhotell", "Varningar", "Alla webbplatser", "Webbplats", "Åtkomst", "Säkerhet", "Schemalagda uppgifter", "Körmiljö", "FTP", "Loggar", "Konto", "Hoppa till innehåll", "Server"},
	"th":    {"เวิร์ดเพรส", "DNS", "โฮสติ้ง", "การแจ้งเตือน", "เว็บไซต์ทั้งหมด", "เว็บไซต์", "การเข้าถึง", "ความปลอดภัย", "งานตามกำหนดเวลา", "สภาพแวดล้อมการทำงาน", "FTP", "บันทึก", "บัญชี", "ข้ามไปยังเนื้อหา", "เซิร์ฟเวอร์"},
	"tr":    {"WordPress", "DNS", "Barındırma", "Uyarılar", "Tüm siteler", "Site", "Erişim", "Güvenlik", "Zamanlanmış görevler", "Çalışma ortamı", "FTP", "Günlükler", "Hesap", "İçeriğe geç", "Sunucu"},
	"uk":    {"WordPress", "DNS", "Хостинг", "Сповіщення", "Усі сайти", "Сайт", "Доступ", "Безпека", "Заплановані завдання", "Середовище виконання", "FTP", "Журнали", "Обліковий запис", "Перейти до вмісту", "Сервер"},
	"vi":    {"WordPress", "DNS", "Lưu trữ web", "Cảnh báo", "Tất cả trang web", "Trang web", "Truy cập", "Bảo mật", "Tác vụ đã lên lịch", "Môi trường chạy", "FTP", "Nhật ký", "Tài khoản", "Chuyển đến nội dung", "Máy chủ"},
	"zh-CN": {"WordPress", "DNS", "托管", "警报", "所有站点", "站点", "访问", "安全", "计划任务", "运行环境", "FTP", "日志", "账户", "跳到内容", "服务器"},
	"zh-TW": {"WordPress", "DNS", "託管", "警示", "所有網站", "網站", "存取", "安全性", "排程工作", "執行環境", "FTP", "記錄", "帳戶", "跳至內容", "伺服器"},
}

var navigationKeys = [...]string{"wordpress", "dns", "hosting", "alerts", "all_sites", "site", "access", "security", "cron", "runtime", "ftp", "logs", "account", "skip_to_content", "server"}

// Action labels combine localized verbs with existing localized feature names.
// Keeping this vocabulary regular gives forms a broad, complete catalog while
// retaining reviewed native words instead of copying English aliases.
var actionKeys = [...]string{"create", "add", "save", "edit", "delete", "enable", "disable", "download", "upload", "restore", "verify", "install", "update"}
var actionWords = map[Locale][13]string{
	English: {"Create", "Add", "Save", "Edit", "Delete", "Enable", "Disable", "Download", "Upload", "Restore", "Verify", "Install", "Update"},
	"ar":    {"إنشاء", "إضافة", "حفظ", "تعديل", "حذف", "تمكين", "تعطيل", "تنزيل", "رفع", "استعادة", "تحقق من", "تثبيت", "تحديث"},
	"bn":    {"তৈরি করুন", "যোগ করুন", "সংরক্ষণ করুন", "সম্পাদনা করুন", "মুছুন", "সক্রিয় করুন", "নিষ্ক্রিয় করুন", "ডাউনলোড করুন", "আপলোড করুন", "পুনরুদ্ধার করুন", "যাচাই করুন", "ইনস্টল করুন", "আপডেট করুন"},
	"ca":    {"Crea", "Afegeix", "Desa", "Edita", "Suprimeix", "Activa", "Desactiva", "Baixa", "Puja", "Restaura", "Verifica", "Instal·la", "Actualitza"},
	"cs":    {"Vytvořit", "Přidat", "Uložit", "Upravit", "Smazat", "Povolit", "Zakázat", "Stáhnout", "Nahrát", "Obnovit", "Ověřit", "Nainstalovat", "Aktualizovat"},
	"da":    {"Opret", "Tilføj", "Gem", "Rediger", "Slet", "Aktivér", "Deaktivér", "Download", "Upload", "Gendan", "Bekræft", "Installér", "Opdater"},
	"de":    {"Erstellen", "Hinzufügen", "Speichern", "Bearbeiten", "Löschen", "Aktivieren", "Deaktivieren", "Herunterladen", "Hochladen", "Wiederherstellen", "Prüfen", "Installieren", "Aktualisieren"},
	"el":    {"Δημιουργία", "Προσθήκη", "Αποθήκευση", "Επεξεργασία", "Διαγραφή", "Ενεργοποίηση", "Απενεργοποίηση", "Λήψη", "Μεταφόρτωση", "Επαναφορά", "Επαλήθευση", "Εγκατάσταση", "Ενημέρωση"},
	"es":    {"Crear", "Añadir", "Guardar", "Editar", "Eliminar", "Activar", "Desactivar", "Descargar", "Subir", "Restaurar", "Verificar", "Instalar", "Actualizar"},
	"fi":    {"Luo", "Lisää", "Tallenna", "Muokkaa", "Poista", "Ota käyttöön", "Poista käytöstä", "Lataa", "Lähetä", "Palauta", "Vahvista", "Asenna", "Päivitä"},
	"fr":    {"Créer", "Ajouter", "Enregistrer", "Modifier", "Supprimer", "Activer", "Désactiver", "Télécharger", "Importer", "Restaurer", "Vérifier", "Installer", "Mettre à jour"},
	"he":    {"יצירה", "הוספה", "שמירה", "עריכה", "מחיקה", "הפעלה", "השבתה", "הורדה", "העלאה", "שחזור", "אימות", "התקנה", "עדכון"},
	"hi":    {"बनाएँ", "जोड़ें", "सहेजें", "संपादित करें", "हटाएँ", "सक्षम करें", "अक्षम करें", "डाउनलोड करें", "अपलोड करें", "पुनर्स्थापित करें", "सत्यापित करें", "इंस्टॉल करें", "अपडेट करें"},
	"hr":    {"Izradi", "Dodaj", "Spremi", "Uredi", "Izbriši", "Omogući", "Onemogući", "Preuzmi", "Prenesi", "Vrati", "Provjeri", "Instaliraj", "Ažuriraj"},
	"hu":    {"Létrehozás", "Hozzáadás", "Mentés", "Szerkesztés", "Törlés", "Engedélyezés", "Letiltás", "Letöltés", "Feltöltés", "Visszaállítás", "Ellenőrzés", "Telepítés", "Frissítés"},
	"id":    {"Buat", "Tambah", "Simpan", "Edit", "Hapus", "Aktifkan", "Nonaktifkan", "Unduh", "Unggah", "Pulihkan", "Verifikasi", "Instal", "Perbarui"},
	"it":    {"Crea", "Aggiungi", "Salva", "Modifica", "Elimina", "Abilita", "Disabilita", "Scarica", "Carica", "Ripristina", "Verifica", "Installa", "Aggiorna"},
	"ja":    {"作成", "追加", "保存", "編集", "削除", "有効化", "無効化", "ダウンロード", "アップロード", "復元", "確認", "インストール", "更新"},
	"ko":    {"만들기", "추가", "저장", "편집", "삭제", "활성화", "비활성화", "다운로드", "업로드", "복원", "확인", "설치", "업데이트"},
	"nl":    {"Maken", "Toevoegen", "Opslaan", "Bewerken", "Verwijderen", "Inschakelen", "Uitschakelen", "Downloaden", "Uploaden", "Herstellen", "Verifiëren", "Installeren", "Bijwerken"},
	"no":    {"Opprett", "Legg til", "Lagre", "Rediger", "Slett", "Aktiver", "Deaktiver", "Last ned", "Last opp", "Gjenopprett", "Bekreft", "Installer", "Oppdater"},
	"pl":    {"Utwórz", "Dodaj", "Zapisz", "Edytuj", "Usuń", "Włącz", "Wyłącz", "Pobierz", "Prześlij", "Przywróć", "Zweryfikuj", "Zainstaluj", "Zaktualizuj"},
	"pt-BR": {"Criar", "Adicionar", "Salvar", "Editar", "Excluir", "Ativar", "Desativar", "Baixar", "Enviar", "Restaurar", "Verificar", "Instalar", "Atualizar"},
	"ro":    {"Creează", "Adaugă", "Salvează", "Editează", "Șterge", "Activează", "Dezactivează", "Descarcă", "Încarcă", "Restaurează", "Verifică", "Instalează", "Actualizează"},
	"ru":    {"Создать", "Добавить", "Сохранить", "Изменить", "Удалить", "Включить", "Отключить", "Скачать", "Загрузить", "Восстановить", "Проверить", "Установить", "Обновить"},
	"sk":    {"Vytvoriť", "Pridať", "Uložiť", "Upraviť", "Odstrániť", "Povoliť", "Zakázať", "Stiahnuť", "Nahrať", "Obnoviť", "Overiť", "Nainštalovať", "Aktualizovať"},
	"sv":    {"Skapa", "Lägg till", "Spara", "Redigera", "Ta bort", "Aktivera", "Inaktivera", "Ladda ned", "Ladda upp", "Återställ", "Verifiera", "Installera", "Uppdatera"},
	"th":    {"สร้าง", "เพิ่ม", "บันทึก", "แก้ไข", "ลบ", "เปิดใช้", "ปิดใช้", "ดาวน์โหลด", "อัปโหลด", "กู้คืน", "ตรวจสอบ", "ติดตั้ง", "อัปเดต"},
	"tr":    {"Oluştur", "Ekle", "Kaydet", "Düzenle", "Sil", "Etkinleştir", "Devre dışı bırak", "İndir", "Yükle", "Geri yükle", "Doğrula", "Kur", "Güncelle"},
	"uk":    {"Створити", "Додати", "Зберегти", "Редагувати", "Видалити", "Увімкнути", "Вимкнути", "Завантажити", "Вивантажити", "Відновити", "Перевірити", "Встановити", "Оновити"},
	"vi":    {"Tạo", "Thêm", "Lưu", "Chỉnh sửa", "Xóa", "Bật", "Tắt", "Tải xuống", "Tải lên", "Khôi phục", "Xác minh", "Cài đặt", "Cập nhật"},
	"zh-CN": {"创建", "添加", "保存", "编辑", "删除", "启用", "禁用", "下载", "上传", "恢复", "验证", "安装", "更新"},
	"zh-TW": {"建立", "新增", "儲存", "編輯", "刪除", "啟用", "停用", "下載", "上傳", "還原", "驗證", "安裝", "更新"},
}

var fieldKeys = [...]string{"name", "status", "label", "username", "password", "email", "host", "port", "sender", "recipient", "subject", "threshold", "cpu", "memory", "disk", "service", "certificate", "expiry", "enabled", "disabled", "active", "failed", "pending", "cancel", "copy", "cut", "paste", "rename", "search", "size"}

var fieldWords = map[Locale]string{
	English: "Name|Status|Label|Username|Password|Email|Host|Port|Sender|Recipient|Subject|Threshold|CPU|Memory|Disk|Service|Certificate|Expiry|Enabled|Disabled|Active|Failed|Pending|Cancel|Copy|Cut|Paste|Rename|Search|Size",
	"ar":    "الاسم|الحالة|التسمية|اسم المستخدم|كلمة المرور|البريد الإلكتروني|المضيف|المنفذ|المرسل|المستلم|الموضوع|الحد|المعالج|الذاكرة|القرص|الخدمة|الشهادة|انتهاء الصلاحية|مُمكّن|معطّل|نشط|فشل|قيد الانتظار|إلغاء|نسخ|قص|لصق|إعادة تسمية|بحث|الحجم",
	"bn":    "নাম|অবস্থা|লেবেল|ব্যবহারকারীর নাম|পাসওয়ার্ড|ইমেইল|হোস্ট|পোর্ট|প্রেরক|প্রাপক|বিষয়|সীমা|CPU|মেমরি|ডিস্ক|পরিষেবা|সার্টিফিকেট|মেয়াদ শেষ|সক্রিয়|নিষ্ক্রিয়|চালু|ব্যর্থ|অপেক্ষমাণ|বাতিল|কপি|কাট|পেস্ট|নাম পরিবর্তন|অনুসন্ধান|আকার",
	"ca":    "Nom|Estat|Etiqueta|Nom d’usuari|Contrasenya|Correu electrònic|Amfitrió|Port|Remitent|Destinatari|Assumpte|Llindar|CPU|Memòria|Disc|Servei|Certificat|Caducitat|Activat|Desactivat|Actiu|Fallat|Pendent|Cancel·la|Copia|Retalla|Enganxa|Canvia el nom|Cerca|Mida",
	"cs":    "Název|Stav|Štítek|Uživatelské jméno|Heslo|E-mail|Hostitel|Port|Odesílatel|Příjemce|Předmět|Prahová hodnota|CPU|Paměť|Disk|Služba|Certifikát|Platnost do|Povoleno|Zakázáno|Aktivní|Selhalo|Čeká|Zrušit|Kopírovat|Vyjmout|Vložit|Přejmenovat|Hledat|Velikost",
	"da":    "Navn|Status|Etiket|Brugernavn|Adgangskode|E-mail|Vært|Port|Afsender|Modtager|Emne|Tærskel|CPU|Hukommelse|Disk|Tjeneste|Certifikat|Udløb|Aktiveret|Deaktiveret|Aktiv|Mislykket|Afventer|Annuller|Kopiér|Klip|Indsæt|Omdøb|Søg|Størrelse",
	"de":    "Name|Status|Bezeichnung|Benutzername|Passwort|E-Mail|Host|Port|Absender|Empfänger|Betreff|Schwellenwert|CPU|Arbeitsspeicher|Festplatte|Dienst|Zertifikat|Ablauf|Aktiviert|Deaktiviert|Aktiv|Fehlgeschlagen|Ausstehend|Abbrechen|Kopieren|Ausschneiden|Einfügen|Umbenennen|Suchen|Größe",
	"el":    "Όνομα|Κατάσταση|Ετικέτα|Όνομα χρήστη|Κωδικός πρόσβασης|Email|Κεντρικός υπολογιστής|Θύρα|Αποστολέας|Παραλήπτης|Θέμα|Όριο|CPU|Μνήμη|Δίσκος|Υπηρεσία|Πιστοποιητικό|Λήξη|Ενεργοποιημένο|Απενεργοποιημένο|Ενεργό|Απέτυχε|Σε αναμονή|Ακύρωση|Αντιγραφή|Αποκοπή|Επικόλληση|Μετονομασία|Αναζήτηση|Μέγεθος",
	"es":    "Nombre|Estado|Etiqueta|Nombre de usuario|Contraseña|Correo electrónico|Host|Puerto|Remitente|Destinatario|Asunto|Umbral|CPU|Memoria|Disco|Servicio|Certificado|Caducidad|Activado|Desactivado|Activo|Fallido|Pendiente|Cancelar|Copiar|Cortar|Pegar|Renombrar|Buscar|Tamaño",
	"fi":    "Nimi|Tila|Tunniste|Käyttäjänimi|Salasana|Sähköposti|Isäntä|Portti|Lähettäjä|Vastaanottaja|Aihe|Raja|CPU|Muisti|Levy|Palvelu|Varmenne|Vanhentuminen|Käytössä|Poistettu käytöstä|Aktiivinen|Epäonnistui|Odottaa|Peruuta|Kopioi|Leikkaa|Liitä|Nimeä uudelleen|Hae|Koko",
	"fr":    "Nom|État|Libellé|Nom d’utilisateur|Mot de passe|E-mail|Hôte|Port|Expéditeur|Destinataire|Objet|Seuil|CPU|Mémoire|Disque|Service|Certificat|Expiration|Activé|Désactivé|Actif|Échec|En attente|Annuler|Copier|Couper|Coller|Renommer|Rechercher|Taille",
	"he":    "שם|מצב|תווית|שם משתמש|סיסמה|דוא״ל|מארח|יציאה|שולח|נמען|נושא|סף|מעבד|זיכרון|דיסק|שירות|אישור|תפוגה|מופעל|מושבת|פעיל|נכשל|ממתין|ביטול|העתקה|גזירה|הדבקה|שינוי שם|חיפוש|גודל",
	"hi":    "नाम|स्थिति|लेबल|उपयोगकर्ता नाम|पासवर्ड|ईमेल|होस्ट|पोर्ट|प्रेषक|प्राप्तकर्ता|विषय|सीमा|CPU|मेमोरी|डिस्क|सेवा|प्रमाणपत्र|समाप्ति|सक्षम|अक्षम|सक्रिय|विफल|लंबित|रद्द करें|कॉपी|कट|पेस्ट|नाम बदलें|खोजें|आकार",
	"hr":    "Naziv|Status|Oznaka|Korisničko ime|Lozinka|E-pošta|Poslužitelj|Priključak|Pošiljatelj|Primatelj|Predmet|Prag|CPU|Memorija|Disk|Usluga|Certifikat|Istek|Omogućeno|Onemogućeno|Aktivno|Neuspjelo|Na čekanju|Odustani|Kopiraj|Izreži|Zalijepi|Preimenuj|Traži|Veličina",
	"hu":    "Név|Állapot|Címke|Felhasználónév|Jelszó|E-mail|Gazdagép|Port|Feladó|Címzett|Tárgy|Küszöb|CPU|Memória|Lemez|Szolgáltatás|Tanúsítvány|Lejárat|Engedélyezve|Letiltva|Aktív|Sikertelen|Függőben|Mégse|Másolás|Kivágás|Beillesztés|Átnevezés|Keresés|Méret",
	"id":    "Nama|Status|Label|Nama pengguna|Kata sandi|Email|Host|Port|Pengirim|Penerima|Subjek|Ambang|CPU|Memori|Disk|Layanan|Sertifikat|Kedaluwarsa|Diaktifkan|Dinonaktifkan|Aktif|Gagal|Tertunda|Batal|Salin|Potong|Tempel|Ubah nama|Cari|Ukuran",
	"it":    "Nome|Stato|Etichetta|Nome utente|Password|Email|Host|Porta|Mittente|Destinatario|Oggetto|Soglia|CPU|Memoria|Disco|Servizio|Certificato|Scadenza|Abilitato|Disabilitato|Attivo|Non riuscito|In sospeso|Annulla|Copia|Taglia|Incolla|Rinomina|Cerca|Dimensione",
	"ja":    "名前|状態|ラベル|ユーザー名|パスワード|メール|ホスト|ポート|送信者|受信者|件名|しきい値|CPU|メモリ|ディスク|サービス|証明書|有効期限|有効|無効|稼働中|失敗|保留中|キャンセル|コピー|切り取り|貼り付け|名前を変更|検索|サイズ",
	"ko":    "이름|상태|레이블|사용자 이름|비밀번호|이메일|호스트|포트|보낸 사람|받는 사람|제목|임계값|CPU|메모리|디스크|서비스|인증서|만료|활성화됨|비활성화됨|활성|실패|대기 중|취소|복사|잘라내기|붙여넣기|이름 바꾸기|검색|크기",
	"nl":    "Naam|Status|Label|Gebruikersnaam|Wachtwoord|E-mail|Host|Poort|Afzender|Ontvanger|Onderwerp|Drempel|CPU|Geheugen|Schijf|Dienst|Certificaat|Vervaldatum|Ingeschakeld|Uitgeschakeld|Actief|Mislukt|In afwachting|Annuleren|Kopiëren|Knippen|Plakken|Hernoemen|Zoeken|Grootte",
	"no":    "Navn|Status|Etikett|Brukernavn|Passord|E-post|Vert|Port|Avsender|Mottaker|Emne|Terskel|CPU|Minne|Disk|Tjeneste|Sertifikat|Utløp|Aktivert|Deaktivert|Aktiv|Mislyktes|Venter|Avbryt|Kopier|Klipp ut|Lim inn|Gi nytt navn|Søk|Størrelse",
	"pl":    "Nazwa|Stan|Etykieta|Nazwa użytkownika|Hasło|E-mail|Host|Port|Nadawca|Odbiorca|Temat|Próg|CPU|Pamięć|Dysk|Usługa|Certyfikat|Wygaśnięcie|Włączone|Wyłączone|Aktywne|Niepowodzenie|Oczekujące|Anuluj|Kopiuj|Wytnij|Wklej|Zmień nazwę|Szukaj|Rozmiar",
	"pt-BR": "Nome|Status|Rótulo|Nome de usuário|Senha|Email|Host|Porta|Remetente|Destinatário|Assunto|Limite|CPU|Memória|Disco|Serviço|Certificado|Validade|Ativado|Desativado|Ativo|Falhou|Pendente|Cancelar|Copiar|Recortar|Colar|Renomear|Pesquisar|Tamanho",
	"ro":    "Nume|Stare|Etichetă|Nume de utilizator|Parolă|E-mail|Gazdă|Port|Expeditor|Destinatar|Subiect|Prag|CPU|Memorie|Disc|Serviciu|Certificat|Expirare|Activat|Dezactivat|Activ|Eșuat|În așteptare|Anulează|Copiază|Decupează|Lipește|Redenumește|Caută|Dimensiune",
	"ru":    "Имя|Состояние|Метка|Имя пользователя|Пароль|Эл. почта|Хост|Порт|Отправитель|Получатель|Тема|Порог|ЦП|Память|Диск|Служба|Сертификат|Истечение|Включено|Отключено|Активно|Ошибка|Ожидание|Отмена|Копировать|Вырезать|Вставить|Переименовать|Поиск|Размер",
	"sk":    "Názov|Stav|Štítok|Používateľské meno|Heslo|E-mail|Hostiteľ|Port|Odosielateľ|Príjemca|Predmet|Prahová hodnota|CPU|Pamäť|Disk|Služba|Certifikát|Platnosť do|Povolené|Zakázané|Aktívne|Zlyhalo|Čaká|Zrušiť|Kopírovať|Vystrihnúť|Prilepiť|Premenovať|Hľadať|Veľkosť",
	"sv":    "Namn|Status|Etikett|Användarnamn|Lösenord|E-post|Värd|Port|Avsändare|Mottagare|Ämne|Tröskel|CPU|Minne|Disk|Tjänst|Certifikat|Utgång|Aktiverad|Inaktiverad|Aktiv|Misslyckad|Väntande|Avbryt|Kopiera|Klipp ut|Klistra in|Byt namn|Sök|Storlek",
	"th":    "ชื่อ|สถานะ|ป้ายกำกับ|ชื่อผู้ใช้|รหัสผ่าน|อีเมล|โฮสต์|พอร์ต|ผู้ส่ง|ผู้รับ|หัวเรื่อง|เกณฑ์|CPU|หน่วยความจำ|ดิสก์|บริการ|ใบรับรอง|วันหมดอายุ|เปิดใช้|ปิดใช้|ใช้งานอยู่|ล้มเหลว|รอดำเนินการ|ยกเลิก|คัดลอก|ตัด|วาง|เปลี่ยนชื่อ|ค้นหา|ขนาด",
	"tr":    "Ad|Durum|Etiket|Kullanıcı adı|Parola|E-posta|Ana makine|Bağlantı noktası|Gönderen|Alıcı|Konu|Eşik|CPU|Bellek|Disk|Hizmet|Sertifika|Son kullanma|Etkin|Devre dışı|Aktif|Başarısız|Beklemede|İptal|Kopyala|Kes|Yapıştır|Yeniden adlandır|Ara|Boyut",
	"uk":    "Назва|Стан|Мітка|Ім’я користувача|Пароль|Ел. пошта|Хост|Порт|Відправник|Одержувач|Тема|Поріг|ЦП|Пам’ять|Диск|Служба|Сертифікат|Закінчення|Увімкнено|Вимкнено|Активно|Помилка|Очікування|Скасувати|Копіювати|Вирізати|Вставити|Перейменувати|Пошук|Розмір",
	"vi":    "Tên|Trạng thái|Nhãn|Tên người dùng|Mật khẩu|Email|Máy chủ|Cổng|Người gửi|Người nhận|Chủ đề|Ngưỡng|CPU|Bộ nhớ|Ổ đĩa|Dịch vụ|Chứng chỉ|Hết hạn|Đã bật|Đã tắt|Đang hoạt động|Thất bại|Đang chờ|Hủy|Sao chép|Cắt|Dán|Đổi tên|Tìm kiếm|Kích thước",
	"zh-CN": "名称|状态|标签|用户名|密码|电子邮件|主机|端口|发件人|收件人|主题|阈值|CPU|内存|磁盘|服务|证书|到期时间|已启用|已禁用|活动|失败|待处理|取消|复制|剪切|粘贴|重命名|搜索|大小",
	"zh-TW": "名稱|狀態|標籤|使用者名稱|密碼|電子郵件|主機|連接埠|寄件者|收件者|主旨|閾值|CPU|記憶體|磁碟|服務|憑證|到期時間|已啟用|已停用|作用中|失敗|待處理|取消|複製|剪下|貼上|重新命名|搜尋|大小",
}

var fileKeys = [...]string{"new_folder", "archive", "extract", "refresh", "more_actions", "clear_selection", "no_selection", "selected", "select_all", "directory", "uploads", "clear_finished", "close"}

var fileWords = map[Locale]string{
	English: "New folder|Archive|Extract|Refresh|More actions|Clear selection|No selection|Selected|Select all|Directory|Uploads|Clear finished|Close",
	"ar":    "مجلد جديد|أرشفة|استخراج|تحديث|إجراءات إضافية|مسح التحديد|لا يوجد تحديد|محدد|تحديد الكل|الدليل|عمليات الرفع|مسح المكتمل|إغلاق",
	"bn":    "নতুন ফোল্ডার|আর্কাইভ|এক্সট্র্যাক্ট|রিফ্রেশ|আরও কাজ|নির্বাচন মুছুন|কিছু নির্বাচিত নয়|নির্বাচিত|সব নির্বাচন করুন|ডিরেক্টরি|আপলোড|সম্পন্নগুলো মুছুন|বন্ধ করুন",
	"ca":    "Carpeta nova|Arxiva|Extreu|Actualitza|Més accions|Neteja la selecció|Cap selecció|Seleccionats|Selecciona-ho tot|Directori|Càrregues|Neteja els acabats|Tanca",
	"cs":    "Nová složka|Archivovat|Rozbalit|Obnovit|Další akce|Zrušit výběr|Nic není vybráno|Vybráno|Vybrat vše|Adresář|Nahrávání|Vymazat dokončené|Zavřít",
	"da":    "Ny mappe|Arkivér|Udpak|Opdater|Flere handlinger|Ryd markering|Intet valgt|Valgt|Vælg alle|Mappe|Uploads|Ryd færdige|Luk",
	"de":    "Neuer Ordner|Archivieren|Entpacken|Aktualisieren|Weitere Aktionen|Auswahl aufheben|Keine Auswahl|Ausgewählt|Alle auswählen|Verzeichnis|Uploads|Fertige entfernen|Schließen",
	"el":    "Νέος φάκελος|Αρχειοθέτηση|Εξαγωγή|Ανανέωση|Περισσότερες ενέργειες|Εκκαθάριση επιλογής|Καμία επιλογή|Επιλεγμένα|Επιλογή όλων|Κατάλογος|Μεταφορτώσεις|Εκκαθάριση ολοκληρωμένων|Κλείσιμο",
	"es":    "Nueva carpeta|Archivar|Extraer|Actualizar|Más acciones|Borrar selección|Sin selección|Seleccionados|Seleccionar todo|Directorio|Subidas|Borrar finalizadas|Cerrar",
	"fi":    "Uusi kansio|Arkistoi|Pura|Päivitä|Lisää toimintoja|Tyhjennä valinta|Ei valintaa|Valittu|Valitse kaikki|Hakemisto|Lähetykset|Tyhjennä valmiit|Sulje",
	"fr":    "Nouveau dossier|Archiver|Extraire|Actualiser|Plus d’actions|Effacer la sélection|Aucune sélection|Sélectionnés|Tout sélectionner|Répertoire|Importations|Effacer les terminés|Fermer",
	"he":    "תיקייה חדשה|יצירת ארכיון|חילוץ|רענון|פעולות נוספות|ניקוי הבחירה|אין בחירה|נבחרו|בחירת הכול|תיקייה|העלאות|ניקוי שהושלמו|סגירה",
	"hi":    "नया फ़ोल्डर|संग्रहित करें|निकालें|रीफ़्रेश करें|और कार्रवाइयाँ|चयन साफ़ करें|कोई चयन नहीं|चयनित|सभी चुनें|डायरेक्टरी|अपलोड|पूर्ण हटाएँ|बंद करें",
	"hr":    "Nova mapa|Arhiviraj|Raspakiraj|Osvježi|Više radnji|Poništi odabir|Ništa nije odabrano|Odabrano|Odaberi sve|Direktorij|Prijenosi|Ukloni dovršeno|Zatvori",
	"hu":    "Új mappa|Archiválás|Kibontás|Frissítés|További műveletek|Kijelölés törlése|Nincs kijelölés|Kijelölve|Összes kijelölése|Könyvtár|Feltöltések|Befejezettek törlése|Bezárás",
	"id":    "Folder baru|Arsipkan|Ekstrak|Segarkan|Tindakan lainnya|Hapus pilihan|Tidak ada pilihan|Dipilih|Pilih semua|Direktori|Unggahan|Hapus yang selesai|Tutup",
	"it":    "Nuova cartella|Archivia|Estrai|Aggiorna|Altre azioni|Cancella selezione|Nessuna selezione|Selezionati|Seleziona tutto|Directory|Caricamenti|Rimuovi completati|Chiudi",
	"ja":    "新しいフォルダー|アーカイブ|展開|更新|その他の操作|選択を解除|選択なし|選択済み|すべて選択|ディレクトリ|アップロード|完了を消去|閉じる",
	"ko":    "새 폴더|보관|압축 풀기|새로 고침|추가 작업|선택 해제|선택 없음|선택됨|모두 선택|디렉터리|업로드|완료 항목 지우기|닫기",
	"nl":    "Nieuwe map|Archiveren|Uitpakken|Vernieuwen|Meer acties|Selectie wissen|Geen selectie|Geselecteerd|Alles selecteren|Map|Uploads|Voltooide wissen|Sluiten",
	"no":    "Ny mappe|Arkiver|Pakk ut|Oppdater|Flere handlinger|Fjern markering|Ingenting valgt|Valgt|Velg alle|Mappe|Opplastinger|Fjern fullførte|Lukk",
	"pl":    "Nowy folder|Archiwizuj|Rozpakuj|Odśwież|Więcej działań|Wyczyść zaznaczenie|Brak zaznaczenia|Wybrano|Zaznacz wszystko|Katalog|Przesyłanie|Wyczyść ukończone|Zamknij",
	"pt-BR": "Nova pasta|Arquivar|Extrair|Atualizar|Mais ações|Limpar seleção|Nenhuma seleção|Selecionados|Selecionar tudo|Diretório|Envios|Limpar concluídos|Fechar",
	"ro":    "Dosar nou|Arhivează|Extrage|Reîmprospătează|Mai multe acțiuni|Șterge selecția|Nicio selecție|Selectate|Selectează tot|Director|Încărcări|Șterge finalizate|Închide",
	"ru":    "Новая папка|Архивировать|Извлечь|Обновить|Другие действия|Снять выделение|Ничего не выбрано|Выбрано|Выбрать всё|Каталог|Загрузки|Очистить завершённые|Закрыть",
	"sk":    "Nový priečinok|Archivovať|Rozbaliť|Obnoviť|Ďalšie akcie|Zrušiť výber|Nič nie je vybrané|Vybrané|Vybrať všetko|Priečinok|Nahrávania|Vymazať dokončené|Zavrieť",
	"sv":    "Ny mapp|Arkivera|Packa upp|Uppdatera|Fler åtgärder|Rensa markering|Inget valt|Valda|Markera alla|Katalog|Uppladdningar|Rensa slutförda|Stäng",
	"th":    "โฟลเดอร์ใหม่|เก็บถาวร|แตกไฟล์|รีเฟรช|การดำเนินการเพิ่มเติม|ล้างการเลือก|ไม่มีการเลือก|เลือกแล้ว|เลือกทั้งหมด|ไดเรกทอรี|การอัปโหลด|ล้างรายการที่เสร็จแล้ว|ปิด",
	"tr":    "Yeni klasör|Arşivle|Çıkart|Yenile|Diğer işlemler|Seçimi temizle|Seçim yok|Seçildi|Tümünü seç|Dizin|Yüklemeler|Tamamlananları temizle|Kapat",
	"uk":    "Нова папка|Архівувати|Розпакувати|Оновити|Інші дії|Очистити вибір|Нічого не вибрано|Вибрано|Вибрати все|Каталог|Вивантаження|Очистити завершені|Закрити",
	"vi":    "Thư mục mới|Lưu trữ|Giải nén|Làm mới|Thao tác khác|Xóa lựa chọn|Chưa chọn|Đã chọn|Chọn tất cả|Thư mục|Tệp tải lên|Xóa mục hoàn tất|Đóng",
	"zh-CN": "新建文件夹|归档|解压|刷新|更多操作|清除选择|未选择|已选择|全选|目录|上传|清除已完成|关闭",
	"zh-TW": "新增資料夾|封存|解壓縮|重新整理|更多操作|清除選取|未選取|已選取|全選|目錄|上傳|清除已完成|關閉",
}

var commonKeys = [...]string{"appearance", "preferences", "system", "light", "dark", "ip_address", "http_status", "http_method", "path", "all_requests", "errors", "apply_filters", "clear_filters", "schedule", "account_settings", "slack", "telegram"}

var commonWords = map[Locale]string{
	English: "Appearance|Preferences|System|Light|Dark|IP address|HTTP status|HTTP method|Path|All requests|Errors|Apply filters|Clear filters|Schedule|Account settings|Slack|Telegram",
	"ar":    "المظهر|التفضيلات|النظام|فاتح|داكن|عنوان IP|حالة HTTP|طريقة HTTP|المسار|كل الطلبات|الأخطاء|تطبيق عوامل التصفية|مسح عوامل التصفية|الجدول|إعدادات الحساب|Slack|Telegram",
	"bn":    "চেহারা|পছন্দসমূহ|সিস্টেম|হালকা|গাঢ়|IP ঠিকানা|HTTP অবস্থা|HTTP পদ্ধতি|পথ|সব অনুরোধ|ত্রুটি|ফিল্টার প্রয়োগ করুন|ফিল্টার মুছুন|সময়সূচি|অ্যাকাউন্ট সেটিংস|Slack|Telegram",
	"ca":    "Aparença|Preferències|Sistema|Clar|Fosc|Adreça IP|Estat HTTP|Mètode HTTP|Camí|Totes les sol·licituds|Errors|Aplica els filtres|Neteja els filtres|Programació|Configuració del compte|Slack|Telegram",
	"cs":    "Vzhled|Předvolby|Systém|Světlý|Tmavý|IP adresa|Stav HTTP|Metoda HTTP|Cesta|Všechny požadavky|Chyby|Použít filtry|Vymazat filtry|Plán|Nastavení účtu|Slack|Telegram",
	"da":    "Udseende|Præferencer|System|Lys|Mørk|IP-adresse|HTTP-status|HTTP-metode|Sti|Alle anmodninger|Fejl|Anvend filtre|Ryd filtre|Tidsplan|Kontoindstillinger|Slack|Telegram",
	"de":    "Darstellung|Präferenzen|System|Hell|Dunkel|IP-Adresse|HTTP-Status|HTTP-Methode|Pfad|Alle Anfragen|Fehler|Filter anwenden|Filter zurücksetzen|Zeitplan|Kontoeinstellungen|Slack|Telegram",
	"el":    "Εμφάνιση|Προτιμήσεις|Σύστημα|Φωτεινό|Σκούρο|Διεύθυνση IP|Κατάσταση HTTP|Μέθοδος HTTP|Διαδρομή|Όλα τα αιτήματα|Σφάλματα|Εφαρμογή φίλτρων|Εκκαθάριση φίλτρων|Πρόγραμμα|Ρυθμίσεις λογαριασμού|Slack|Telegram",
	"es":    "Apariencia|Preferencias|Sistema|Claro|Oscuro|Dirección IP|Estado HTTP|Método HTTP|Ruta|Todas las solicitudes|Errores|Aplicar filtros|Borrar filtros|Programación|Configuración de la cuenta|Slack|Telegram",
	"fi":    "Ulkoasu|Määritykset|Järjestelmä|Vaalea|Tumma|IP-osoite|HTTP-tila|HTTP-menetelmä|Polku|Kaikki pyynnöt|Virheet|Käytä suodattimia|Tyhjennä suodattimet|Aikataulu|Tilin asetukset|Slack|Telegram",
	"fr":    "Apparence|Préférences|Système|Clair|Sombre|Adresse IP|Statut HTTP|Méthode HTTP|Chemin|Toutes les requêtes|Erreurs|Appliquer les filtres|Effacer les filtres|Planification|Paramètres du compte|Slack|Telegram",
	"he":    "מראה|העדפות|מערכת|בהיר|כהה|כתובת IP|מצב HTTP|שיטת HTTP|נתיב|כל הבקשות|שגיאות|החלת מסננים|ניקוי מסננים|לוח זמנים|הגדרות חשבון|Slack|Telegram",
	"hi":    "रूप-रंग|प्राथमिकताएँ|सिस्टम|हल्का|गहरा|IP पता|HTTP स्थिति|HTTP विधि|पथ|सभी अनुरोध|त्रुटियाँ|फ़िल्टर लागू करें|फ़िल्टर साफ़ करें|अनुसूची|खाता सेटिंग|Slack|Telegram",
	"hr":    "Izgled|Postavke|Sustav|Svijetlo|Tamno|IP adresa|HTTP status|HTTP metoda|Putanja|Svi zahtjevi|Pogreške|Primijeni filtre|Očisti filtre|Raspored|Postavke računa|Slack|Telegram",
	"hu":    "Megjelenés|Beállítások|Rendszer|Világos|Sötét|IP-cím|HTTP-állapot|HTTP-metódus|Elérési út|Minden kérés|Hibák|Szűrők alkalmazása|Szűrők törlése|Ütemezés|Fiókbeállítások|Slack|Telegram",
	"id":    "Tampilan|Preferensi|Sistem|Terang|Gelap|Alamat IP|Status HTTP|Metode HTTP|Jalur|Semua permintaan|Kesalahan|Terapkan filter|Hapus filter|Jadwal|Pengaturan akun|Slack|Telegram",
	"it":    "Aspetto|Preferenze|Sistema|Chiaro|Scuro|Indirizzo IP|Stato HTTP|Metodo HTTP|Percorso|Tutte le richieste|Errori|Applica filtri|Cancella filtri|Pianificazione|Impostazioni account|Slack|Telegram",
	"ja":    "外観|設定|システム|ライト|ダーク|IPアドレス|HTTPステータス|HTTPメソッド|パス|すべてのリクエスト|エラー|フィルターを適用|フィルターをクリア|スケジュール|アカウント設定|Slack|Telegram",
	"ko":    "모양|환경설정|시스템|라이트|다크|IP 주소|HTTP 상태|HTTP 메서드|경로|모든 요청|오류|필터 적용|필터 지우기|일정|계정 설정|Slack|Telegram",
	"nl":    "Weergave|Voorkeuren|Systeem|Licht|Donker|IP-adres|HTTP-status|HTTP-methode|Pad|Alle verzoeken|Fouten|Filters toepassen|Filters wissen|Planning|Accountinstellingen|Slack|Telegram",
	"no":    "Utseende|Innstillinger|System|Lys|Mørk|IP-adresse|HTTP-status|HTTP-metode|Sti|Alle forespørsler|Feil|Bruk filtre|Tøm filtre|Tidsplan|Kontoinnstillinger|Slack|Telegram",
	"pl":    "Wygląd|Preferencje|System|Jasny|Ciemny|Adres IP|Status HTTP|Metoda HTTP|Ścieżka|Wszystkie żądania|Błędy|Zastosuj filtry|Wyczyść filtry|Harmonogram|Ustawienia konta|Slack|Telegram",
	"pt-BR": "Aparência|Preferências|Sistema|Claro|Escuro|Endereço IP|Status HTTP|Método HTTP|Caminho|Todas as solicitações|Erros|Aplicar filtros|Limpar filtros|Agendamento|Configurações da conta|Slack|Telegram",
	"ro":    "Aspect|Preferințe|Sistem|Luminos|Întunecat|Adresă IP|Stare HTTP|Metodă HTTP|Cale|Toate solicitările|Erori|Aplică filtrele|Șterge filtrele|Program|Setări cont|Slack|Telegram",
	"ru":    "Оформление|Предпочтения|Система|Светлая|Тёмная|IP-адрес|Статус HTTP|Метод HTTP|Путь|Все запросы|Ошибки|Применить фильтры|Сбросить фильтры|Расписание|Настройки учётной записи|Slack|Telegram",
	"sk":    "Vzhľad|Predvoľby|Systém|Svetlý|Tmavý|IP adresa|Stav HTTP|Metóda HTTP|Cesta|Všetky požiadavky|Chyby|Použiť filtre|Vymazať filtre|Plán|Nastavenia účtu|Slack|Telegram",
	"sv":    "Utseende|Inställningar|System|Ljust|Mörkt|IP-adress|HTTP-status|HTTP-metod|Sökväg|Alla förfrågningar|Fel|Tillämpa filter|Rensa filter|Schema|Kontoinställningar|Slack|Telegram",
	"th":    "รูปลักษณ์|การกำหนดลักษณะ|ระบบ|สว่าง|มืด|ที่อยู่ IP|สถานะ HTTP|เมธอด HTTP|เส้นทาง|คำขอทั้งหมด|ข้อผิดพลาด|ใช้ตัวกรอง|ล้างตัวกรอง|กำหนดการ|การตั้งค่าบัญชี|Slack|Telegram",
	"tr":    "Görünüm|Tercihler|Sistem|Açık|Koyu|IP adresi|HTTP durumu|HTTP yöntemi|Yol|Tüm istekler|Hatalar|Filtreleri uygula|Filtreleri temizle|Zamanlama|Hesap ayarları|Slack|Telegram",
	"uk":    "Вигляд|Уподобання|Система|Світла|Темна|IP-адреса|Статус HTTP|Метод HTTP|Шлях|Усі запити|Помилки|Застосувати фільтри|Очистити фільтри|Розклад|Налаштування облікового запису|Slack|Telegram",
	"vi":    "Giao diện|Tùy chọn|Hệ thống|Sáng|Tối|Địa chỉ IP|Trạng thái HTTP|Phương thức HTTP|Đường dẫn|Tất cả yêu cầu|Lỗi|Áp dụng bộ lọc|Xóa bộ lọc|Lịch biểu|Cài đặt tài khoản|Slack|Telegram",
	"zh-CN": "外观|偏好设置|系统|浅色|深色|IP 地址|HTTP 状态|HTTP 方法|路径|所有请求|错误|应用筛选条件|清除筛选条件|计划|账户设置|Slack|Telegram",
	"zh-TW": "外觀|偏好設定|系統|淺色|深色|IP 位址|HTTP 狀態|HTTP 方法|路徑|所有要求|錯誤|套用篩選條件|清除篩選條件|排程|帳戶設定|Slack|Telegram",
}

func init() {
	for locale, values := range selectorWords {
		catalog[locale]["language"] = values[0]
		catalog[locale]["apply"] = values[1]
	}
	for locale, values := range navigationWords {
		for index, key := range navigationKeys {
			catalog[locale][key] = values[index]
		}
	}
	for locale, actions := range actionWords {
		for index, action := range actions {
			catalog[locale][actionKeys[index]] = action
		}
	}
	for locale, joined := range fieldWords {
		values := strings.Split(joined, "|")
		if len(values) != len(fieldKeys) {
			panic("localization field catalog has wrong length for " + string(locale))
		}
		for index, value := range values {
			catalog[locale][fieldKeys[index]] = value
		}
	}
	for locale, joined := range fileWords {
		values := strings.Split(joined, "|")
		if len(values) != len(fileKeys) {
			panic("localization file catalog has wrong length for " + string(locale))
		}
		for index, value := range values {
			catalog[locale][fileKeys[index]] = value
		}
	}
	for locale, joined := range commonWords {
		values := strings.Split(joined, "|")
		if len(values) != len(commonKeys) {
			panic("localization common catalog has wrong length for " + string(locale))
		}
		for index, value := range values {
			catalog[locale][commonKeys[index]] = value
		}
	}
}

func words(values ...string) map[string]string {
	result := make(map[string]string, len(keys))
	for index, value := range values {
		result[keys[index]] = value
	}
	return result
}
